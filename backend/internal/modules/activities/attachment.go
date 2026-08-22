// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"fmt"
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

const attachmentColumns = `at.id, at.entity_type, at.entity_id, at.filename,
	at.content_type, at.byte_size, at.checksum, at.source, at.captured_by, at.created_at,
	at.category, at.title, at.doc_state, at.pinned, at.supersedes_id, at.organization_id,
	at.contract_id`

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
		return auth.EnsureActivityContentVisible(ctx, tx, id)
	}
	return auth.EnsureVisible(ctx, tx, entityType, id)
}

// ensureAttachmentParentWritable is the upload's gate: hanging a file on a
// record changes that record, so the parent must be the caller's to change,
// not merely theirs to read. Out of scope still reads as ErrNotFound; a
// readable parent the caller may not change answers ErrPermissionDenied.
func ensureAttachmentParentWritable(ctx context.Context, tx pgx.Tx, entityType string, id ids.UUID) error {
	if entityType == "activity" {
		return auth.EnsureActivityWritable(ctx, tx, id)
	}
	return auth.EnsureWritable(ctx, tx, entityType, id)
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

// resolveAttachmentParent is the ONE spelling of "find a live attachment's
// parent, then require the caller hold `action` on the parent object type and
// the matching authority over the parent ROW" — GetAttachmentMeta and
// ArchiveAttachment both need exactly this. Object denial and row-scope miss
// both surface as ErrNotFound (existence-hiding), and so does a missing or
// already-archived row: a soft-deleted attachment has no live parent to
// resolve against. OpenAttachment does NOT call this: it fetches storage_key
// in the same round trip, so the key it hands the object store comes from the
// same snapshot the visibility gates just read rather than from a second query
// against a row that may have moved since.
//
// The row gate follows the ACTION rather than being visibility for everyone: a
// read needs only to see the parent, while archiving a file off a record
// changes that record and needs the parent to be the caller's to change. That
// distinction used to be invisible because every parent type a rep could see
// was also one they owned; with a project readable workspace-wide it is the
// difference between "look at another team's file" and "delete it".
func resolveAttachmentParent(ctx context.Context, tx pgx.Tx, id ids.UUID, action principal.Action) (entityType string, err error) {
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
	// This branch is WAIVED in backend/writeauthority_test.go under
	// readAuthorityOnAWritePath:internal/modules/activities:ensureAttachmentParentVisible,
	// because that gate walks the call graph and cannot see which arm a caller
	// takes. The waiver excuses the FUNCTION, so it would keep passing if the
	// visibility probe were ever moved onto a write path — read the waiver
	// before adding a third arm here, and revisit it rather than extend it.
	if action == principal.ActionRead {
		if err := ensureAttachmentParentVisible(ctx, tx, entityType, entityID); err != nil {
			return "", err
		}
		return entityType, nil
	}
	if err := ensureAttachmentParentWritable(ctx, tx, entityType, entityID); err != nil {
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
		var entityType, storageKey string
		var entityID ids.UUID
		row := tx.QueryRow(ctx,
			`SELECT entity_type, entity_id, storage_key FROM attachment WHERE id = $1 AND archived_at IS NULL`, id)
		switch err := row.Scan(&entityType, &entityID, &storageKey); {
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
// like resolveAttachmentParent but without any object-store access:
// the extraction read and the request-access courtesy note both need only
// the row's identity. Archived or invisible reads as ErrNotFound.
func (s *Store) GetAttachmentMeta(ctx context.Context, id ids.UUID) (crmcontracts.Attachment, error) {
	var out crmcontracts.Attachment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := resolveAttachmentParent(ctx, tx, id, principal.ActionRead); err != nil {
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
		entityType, err := resolveAttachmentParent(ctx, tx, id, principal.ActionUpdate)
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
	var out []crmcontracts.Attachment
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, page, err = listAttachments(ctx, tx, entityType, entityID, cursor, limit)
		return err
	})
	return out, page, err
}

// ListAttachmentsTx is ListAttachments inside a caller-opened transaction —
// the composite record read. Same gate, same parent check; only the
// transaction is borrowed.
func (s *Store) ListAttachmentsTx(ctx context.Context, tx pgx.Tx, entityType string, entityID ids.UUID, cursor *string, limit *int) ([]crmcontracts.Attachment, storekit.Page, error) {
	if err := auth.Require(ctx, entityType, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	return listAttachments(ctx, tx, entityType, entityID, cursor, limit)
}

func listAttachments(ctx context.Context, tx pgx.Tx, entityType string, entityID ids.UUID, cursor *string, limit *int) ([]crmcontracts.Attachment, storekit.Page, error) {
	if err := ensureAttachmentParentVisible(ctx, tx, entityType, entityID); err != nil {
		return nil, storekit.Page{}, err
	}
	lim := storekit.ClampLimit(limit)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := sprintf(`at.entity_type = $%d AND at.entity_id = $%d AND at.archived_at IS NULL`,
		arg(entityType), arg(entityID))
	if cursor != nil && *cursor != "" {
		c, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where += sprintf(` AND (at.created_at, at.id) < ($%d, $%d)`, arg(c.CreatedAt), arg(c.ID))
	}
	rows, err := tx.Query(ctx, `SELECT `+attachmentColumns+` FROM attachment at WHERE `+where+
		sprintf(` ORDER BY at.created_at DESC, at.id DESC LIMIT %d`, lim+1), args...)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	defer rows.Close()
	out := []crmcontracts.Attachment{}
	for rows.Next() {
		att, err := scanAttachment(rows)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		out = append(out, att)
	}
	if err := rows.Err(); err != nil {
		return nil, storekit.Page{}, err
	}
	var page storekit.Page
	if len(out) > lim {
		out = out[:lim]
		last := out[len(out)-1]
		page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
	}
	return out, page, nil
}

// rowScanner is the shared Scan surface of pgx.Row and pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func readAttachment(ctx context.Context, tx pgx.Tx, id ids.UUID) (crmcontracts.Attachment, error) {
	return scanAttachment(tx.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachment at WHERE at.id = $1`, id))
}

func scanAttachment(row rowScanner) (crmcontracts.Attachment, error) {
	var cols attachmentScan
	if err := row.Scan(cols.targets()...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return crmcontracts.Attachment{}, apperrors.ErrNotFound
		}
		return crmcontracts.Attachment{}, err
	}
	return cols.attachment(), nil
}

// attachmentScan holds the columns of attachmentColumns as Scan wants them,
// so a read that selects MORE than an attachment (the deal's Files area joins
// the file's origin) can scan one row into these targets plus its own.
type attachmentScan struct {
	att         crmcontracts.Attachment
	aid         ids.UUID
	entityType  string
	entityID    ids.UUID
	contentType *string
	byteSize    *int64
	checksum    *string
	capturedBy  string
	category    string
	docState    string
	supersedes  *ids.UUID
	orgID       *ids.UUID
	contractID  *ids.UUID
}

// targets are the Scan destinations, in attachmentColumns order.
func (c *attachmentScan) targets() []any {
	return []any{
		&c.aid, &c.entityType, &c.entityID, &c.att.Filename,
		&c.contentType, &c.byteSize, &c.checksum, &c.att.Source, &c.capturedBy, &c.att.CreatedAt,
		&c.category, &c.att.Title, &c.docState, &c.att.Pinned, &c.supersedes, &c.orgID, &c.contractID,
	}
}

// attachment builds the wire shape from what was scanned.
func (c *attachmentScan) attachment() crmcontracts.Attachment {
	att := c.att
	att.Id = openapi_types.UUID(c.aid)
	att.EntityId = openapi_types.UUID(c.entityID)
	att.EntityType = crmcontracts.AttachmentEntityType(c.entityType)
	att.ContentType = c.contentType
	att.ByteSize = c.byteSize
	att.Checksum = c.checksum
	capturedBy := c.capturedBy
	att.CapturedBy = &capturedBy
	cat := crmcontracts.AttachmentCategory(c.category)
	att.Category = &cat
	state := crmcontracts.AttachmentDocState(c.docState)
	att.DocState = &state
	att.SupersedesId = uuidOrNil(c.supersedes)
	att.OrganizationId = uuidOrNil(c.orgID)
	att.ContractId = uuidOrNil(c.contractID)
	return att
}

// uuidOrNil maps an absent tenant-local pointer onto the wire's optional uuid
// without inventing a zero id for it.
func uuidOrNil(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	out := openapi_types.UUID(*id)
	return &out
}

// nullIfEmpty maps an absent content-type to a SQL NULL rather than an empty
// string, so the column reflects "unknown" honestly.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ensureContractFileable refuses a contract reference the uploader may not see.
//
// Naming a contract is a read of it, so an uploader who cannot see the
// agreement cannot file paper against it — otherwise the upload is an existence
// oracle, and the document lands under an agreement its owner never chose. A
// contract has no owner column, so its visibility is inherited from the deal it
// came from or its organization; the contracts module owns that rule and this
// asks the same question by joining through it.
//
// Nil is the ordinary case: most client paper is about no particular agreement.
func ensureContractFileable(ctx context.Context, tx pgx.Tx, contractID *ids.UUID) error {
	if contractID == nil {
		return nil
	}
	if err := auth.Require(ctx, "contract", principal.ActionRead); err != nil {
		return err
	}
	var dealID *ids.UUID
	var orgID ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT deal_id, organization_id FROM contract WHERE id = $1 AND archived_at IS NULL`,
		*contractID).Scan(&dealID, &orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Absent, archived, or invisible all answer the same way: a contract
		// the caller cannot reach does not exist as far as they are concerned.
		return apperrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("resolving the contract a document is filed against: %w", err)
	}
	if dealID != nil {
		return auth.EnsureLinkTarget(ctx, tx, "deal", *dealID)
	}
	return auth.EnsureLinkTarget(ctx, tx, "organization", orgID)
}

// accountRollUp resolves the account a newly filed attachment belongs to, which
// is what makes it reachable from the company's document library.
//
// It is a READ PATH, not a second parent: visibility stays the primary parent's
// (see documents.go). Filing it at write time is what the library's index scan
// depends on — a file uploaded without it is invisible to the account view
// forever, and nothing about the upload looks wrong when that happens.
//
// A PERSON rolls up to nothing on purpose. person→organization runs through
// `relationship` with kind 'employment', which is many-valued: a contact who
// works at two companies has no single account, and picking one would file the
// document under a company the uploader never named. A null here means the file
// is reachable from the person, not that it was lost.
func accountRollUp(ctx context.Context, tx pgx.Tx, entityType string, entityID ids.UUID) (ids.UUID, bool, error) {
	switch entityType {
	case linkEntityOrganization:
		return entityID, true, nil
	case linkEntityDeal:
		var org *ids.UUID
		if err := tx.QueryRow(ctx,
			`SELECT organization_id FROM deal WHERE id = $1`, entityID).Scan(&org); err != nil {
			return ids.UUID{}, false, fmt.Errorf("resolving the deal's account for a filed document: %w", err)
		}
		if org == nil {
			return ids.UUID{}, false, nil
		}
		return *org, true, nil
	}
	return ids.UUID{}, false, nil
}
