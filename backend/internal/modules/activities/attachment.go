// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// WithBlobstore returns handlers whose attachment endpoints are backed by
// the given object store. Compose calls this for the roles that serve
// attachments; without it the attachment handlers answer 501.
func (h Handlers) WithBlobstore(blob blobstore.Store) Handlers {
	h.store = h.store.WithBlobstore(blob)
	return h
}

// ErrBlobstoreUnconfigured reports that this process role wired no object
// store, so the attachment endpoints are not available here (the handler
// maps it to 501). A role opts in with Store.WithBlobstore.
var ErrBlobstoreUnconfigured = errors.New("activities: no object store configured")

// AttachmentInput is one upload's server-validated inputs. Body is already
// read (bounded) by the transport; captured_by is stamped from the
// principal in the store, never taken from the request.
type AttachmentInput struct {
	EntityType  string
	EntityID    ids.UUID
	Filename    string
	ContentType string
	Body        []byte
}

const attachmentColumns = `at.id, at.workspace_id, at.entity_type, at.entity_id, at.filename,
	at.content_type, at.byte_size, at.checksum, at.source, at.captured_by, at.created_at, at.scan_status`

// attachmentSource marks how the row was captured; a direct upload is "upload".
const attachmentSource = "upload"

// Audit payload keys for the attachment write shape.
const (
	fieldEntityType = "entity_type"
	fieldEntityID   = "entity_id"
)

// ensureAttachmentParentVisible enforces that the caller can see the parent
// entity the attachment hangs off — an attachment has no independent
// authority, it inherits the parent's row scope. An activity parent scopes
// through the link-walk clause; the owner-scoped entities use the standard
// single-row visibility gate. Out of scope reads as ErrNotFound.
func ensureAttachmentParentVisible(ctx context.Context, tx pgx.Tx, entityType string, id ids.UUID) error {
	if entityType == "activity" {
		return auth.EnsureActivityVisible(ctx, tx, id)
	}
	return auth.EnsureVisible(ctx, tx, entityType, id)
}

// requireParentOrHide checks the parent object grant AFTER the attachment row
// was found (so it exists in this workspace). A caller lacking the grant must
// not learn the attachment exists, so object denial reads as not-found — the
// same 404 a row-scope miss returns (existence-hiding). Upload checks the
// grant before any lookup, so it keeps its plain 403.
func requireParentOrHide(ctx context.Context, entityType string, action principal.Action) error {
	if err := auth.Require(ctx, entityType, action); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return apperrors.ErrNotFound
		}
		return err
	}
	return nil
}

// UploadAttachment stores an object and records its metadata row. Authority
// inherits from the parent entity: the caller must hold Update on the parent
// object type and be able to see the parent row — both are checked BEFORE any
// bytes are written, so an upload to a hidden or cross-tenant entity cannot
// land an object (no storage abuse). The object is put before the row commits
// (a committed row always has its bytes; a failed write leaves at worst an
// orphan object, never a row promising bytes that are not there).
func (s *Store) UploadAttachment(ctx context.Context, in AttachmentInput) (crmcontracts.Attachment, error) {
	if s.blob == nil {
		return crmcontracts.Attachment{}, ErrBlobstoreUnconfigured
	}
	if err := auth.Require(ctx, in.EntityType, principal.ActionUpdate); err != nil {
		return crmcontracts.Attachment{}, err
	}
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		return ensureAttachmentParentVisible(ctx, tx, in.EntityType, in.EntityID)
	}); err != nil {
		return crmcontracts.Attachment{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Attachment{}, err
	}

	id := ids.NewV7()
	key := blobstore.WorkspaceKey(workspaceID(ctx), "attachment", id.String())
	sum := sha256.Sum256(in.Body)
	checksum := hex.EncodeToString(sum[:])
	size := int64(len(in.Body))

	if err := s.blob.Put(ctx, key, bytes.NewReader(in.Body), size, in.ContentType); err != nil {
		return crmcontracts.Attachment{}, err
	}

	var out crmcontracts.Attachment
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO attachment (id, workspace_id, entity_type, entity_id, filename,
				content_type, byte_size, storage_key, checksum, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			id, workspaceID(ctx), in.EntityType, in.EntityID, in.Filename,
			nullIfEmpty(in.ContentType), size, key, checksum, attachmentSource, by); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "create", "attachment", id, nil, map[string]any{
			fieldEntityType: in.EntityType,
			fieldEntityID:   in.EntityID.String(),
			"filename":      in.Filename,
			"byte_size":     size,
		}); err != nil {
			return err
		}
		att, err := readAttachment(ctx, tx, id)
		if err != nil {
			return err
		}
		out = att
		return nil
	})
	return out, err
}

// resolveVisibleAttachmentParent is the ONE spelling of "find a live
// attachment's parent, then require the caller hold `action` on the parent
// object type and be able to see the parent row" — GetAttachmentMeta and
// ArchiveAttachment both need exactly this and nothing more. Object denial
// and row-scope miss both surface as ErrNotFound (existence-hiding), and so
// does a missing or already-archived row: a soft-deleted attachment has no
// live parent to resolve against. OpenAttachment does NOT call this: it
// fetches storage_key/scan_status in the same round trip so its scan-gate
// check reads a single consistent snapshot, rather than opening a second
// query (and a TOCTOU gap) against the row it just gated.
func resolveVisibleAttachmentParent(ctx context.Context, tx pgx.Tx, id ids.UUID, action principal.Action) (entityType string, err error) {
	var entityID ids.UUID
	row := tx.QueryRow(ctx,
		`SELECT entity_type, entity_id FROM attachment WHERE id = $1 AND archived_at IS NULL`, id)
	switch scanErr := row.Scan(&entityType, &entityID); {
	case errors.Is(scanErr, pgx.ErrNoRows):
		return "", apperrors.ErrNotFound
	case scanErr != nil:
		return "", scanErr
	}
	if err := requireParentOrHide(ctx, entityType, action); err != nil {
		return "", err
	}
	if err := ensureAttachmentParentVisible(ctx, tx, entityType, entityID); err != nil {
		return "", err
	}
	return entityType, nil
}

// OpenAttachment resolves a live attachment (row-scoped through its parent)
// and opens its object for reading; the caller closes the reader. Archived
// or invisible attachments read as ErrNotFound.
func (s *Store) OpenAttachment(ctx context.Context, id ids.UUID) (crmcontracts.Attachment, io.ReadCloser, error) {
	if s.blob == nil {
		return crmcontracts.Attachment{}, nil, ErrBlobstoreUnconfigured
	}
	var (
		meta crmcontracts.Attachment
		key  string
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var entityType, storageKey, scanStatus string
		var entityID ids.UUID
		row := tx.QueryRow(ctx,
			`SELECT entity_type, entity_id, storage_key, scan_status FROM attachment WHERE id = $1 AND archived_at IS NULL`, id)
		switch err := row.Scan(&entityType, &entityID, &storageKey, &scanStatus); {
		case errors.Is(err, pgx.ErrNoRows):
			return apperrors.ErrNotFound
		case err != nil:
			return err
		}
		if err := requireParentOrHide(ctx, entityType, principal.ActionRead); err != nil {
			return err
		}
		if err := ensureAttachmentParentVisible(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		// The scan gate refuses the byte stream — after the visibility
		// gates (an invisible row stays a 404, never a scan-state leak)
		// and before any object-store access. Only the stream is withheld;
		// the metadata surfaces keep disclosing the row.
		switch scanStatus {
		case scanStatusScanning:
			return ErrScanPending
		case scanStatusBlocked:
			return ErrAttachmentBlocked
		}
		att, err := readAttachment(ctx, tx, id)
		if err != nil {
			return err
		}
		meta, key = att, storageKey
		return nil
	})
	if err != nil {
		return crmcontracts.Attachment{}, nil, err
	}
	rc, _, err := s.blob.Get(ctx, key)
	if err != nil {
		return crmcontracts.Attachment{}, nil, err
	}
	return meta, rc, nil
}

// GetAttachmentMeta resolves one attachment's metadata row, gated exactly
// like resolveVisibleAttachmentParent but WITHOUT the scan gate or any
// object-store access: the extraction read and request-access courtesy note
// both need only the row's identity, and extraction deliberately ignores
// scan_status (RD-T10 stages evidence from the already-persisted bytes; only
// the raw-byte DOWNLOAD is scan-gated). Archived or invisible reads as
// ErrNotFound.
func (s *Store) GetAttachmentMeta(ctx context.Context, id ids.UUID) (crmcontracts.Attachment, error) {
	var out crmcontracts.Attachment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := resolveVisibleAttachmentParent(ctx, tx, id, principal.ActionRead); err != nil {
			return err
		}
		att, err := readAttachment(ctx, tx, id)
		if err != nil {
			return err
		}
		out = att
		return nil
	})
	return out, err
}

// ArchiveAttachment soft-deletes the row (identical to the module's other
// archive verbs). The object bytes are deliberately retained: authoritative
// byte-erasure is the Art. 17 path, matching how every archived record's data
// persists until erasure. Authority inherits from the parent (Update + row
// scope). Archived/invisible reads as ErrNotFound.
func (s *Store) ArchiveAttachment(ctx context.Context, id ids.UUID) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		entityType, err := resolveVisibleAttachmentParent(ctx, tx, id, principal.ActionUpdate)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE attachment SET archived_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "attachment", id, nil, map[string]any{
			fieldEntityType: entityType,
		})
		return err
	})
}

// ListAttachments returns the live attachments hung off one entity, newest
// first, keyset-paginated. The caller must be able to see the parent entity;
// otherwise the list is ErrNotFound (existence-hiding), never an empty page
// that would confirm the entity exists.
func (s *Store) ListAttachments(ctx context.Context, entityType string, entityID ids.UUID, cursor *string, limit *int) ([]crmcontracts.Attachment, storekit.Page, error) {
	if err := auth.Require(ctx, entityType, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	lim := storekit.ClampLimit(limit)

	var out []crmcontracts.Attachment
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureAttachmentParentVisible(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		args := []any{entityType, entityID}
		where := `at.entity_type = $1 AND at.entity_id = $2 AND at.archived_at IS NULL`
		if cursor != nil && *cursor != "" {
			c, err := storekit.DecodeCursor(*cursor)
			if err != nil {
				return err
			}
			args = append(args, c.CreatedAt, c.ID)
			where += sprintf(` AND (at.created_at, at.id) < ($%d, $%d)`, len(args)-1, len(args))
		}
		rows, err := tx.Query(ctx, `SELECT `+attachmentColumns+` FROM attachment at WHERE `+where+
			sprintf(` ORDER BY at.created_at DESC, at.id DESC LIMIT %d`, lim+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			att, err := scanAttachment(rows)
			if err != nil {
				return err
			}
			out = append(out, att)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > lim {
			out = out[:lim]
			last := out[len(out)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if out == nil {
		out = []crmcontracts.Attachment{}
	}
	return out, page, err
}

// rowScanner is the shared Scan surface of pgx.Row and pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func readAttachment(ctx context.Context, tx pgx.Tx, id ids.UUID) (crmcontracts.Attachment, error) {
	return scanAttachment(tx.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachment at WHERE at.id = $1`, id))
}

func scanAttachment(row rowScanner) (crmcontracts.Attachment, error) {
	var (
		att         crmcontracts.Attachment
		aid, wsID   ids.UUID
		entityType  string
		entityID    ids.UUID
		contentType *string
		byteSize    *int64
		checksum    *string
		capturedBy  string
		scanStatus  string
	)
	if err := row.Scan(&aid, &wsID, &entityType, &entityID, &att.Filename,
		&contentType, &byteSize, &checksum, &att.Source, &capturedBy, &att.CreatedAt, &scanStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return crmcontracts.Attachment{}, apperrors.ErrNotFound
		}
		return crmcontracts.Attachment{}, err
	}
	att.Id = openapi_types.UUID(aid)
	att.WorkspaceId = openapi_types.UUID(wsID)
	att.EntityId = openapi_types.UUID(entityID)
	att.EntityType = crmcontracts.AttachmentEntityType(entityType)
	att.ContentType = contentType
	att.ByteSize = byteSize
	att.Checksum = checksum
	att.CapturedBy = &capturedBy
	status := crmcontracts.AttachmentScanStatus(scanStatus)
	att.ScanStatus = &status
	return att, nil
}

// nullIfEmpty maps an absent content-type to a SQL NULL rather than an empty
// string, so the column reflects "unknown" honestly.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
