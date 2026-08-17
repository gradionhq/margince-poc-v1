// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The attachment WRITE path: what one upload carries, how its bytes are
// fingerprinted and measured, and the transaction that lands the object and the
// row describing it.
//
// Separate from the read side beside it because the two answer different
// questions — this one is about authority and durability before anything
// exists, that one about scope over rows that already do.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AttachmentInput is one upload's server-validated inputs. captured_by is
// stamped from the principal in the store, never taken from the request.
type AttachmentInput struct {
	EntityType  string
	EntityID    ids.UUID
	Filename    string
	ContentType string
	// Content is the file, still unread. A reader rather than bytes so an
	// upload is never resident in full — the transport has already bounded it,
	// and the store reads it twice (hash, then store) by seeking back rather
	// than by keeping a copy. Its length is COUNTED on the hashing pass and
	// never declared alongside it: a size that travels beside the bytes is a
	// size that can disagree with them, and `byte_size` is a column every later
	// reader trusts.
	Content io.ReadSeeker
	// ContractID files this document against one agreement (ADR-0109). A
	// roll-up like the account column beside it, not a second parent: the
	// primary entity still owns the file's visibility.
	ContractID *ids.UUID
}

// digest reads the upload once to fingerprint and measure it, then rewinds so
// the object store can read the same bytes from the start.
//
// Both facts come from the SAME pass. A checksum and a length that were
// established separately can describe different content, and they are what a
// later integrity check compares against — so they are produced together or the
// comparison proves nothing.
func digest(content io.ReadSeeker) (string, int64, error) {
	sum := sha256.New()
	size, err := io.Copy(sum, content)
	if err != nil {
		return "", 0, fmt.Errorf("activities: reading the uploaded file: %w", err)
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("activities: rewinding the uploaded file: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), size, nil
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
		if err := ensureAttachmentParentVisible(ctx, tx, in.EntityType, in.EntityID); err != nil {
			return err
		}
		return ensureContractFileable(ctx, tx, in.ContractID)
	}); err != nil {
		return crmcontracts.Attachment{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Attachment{}, err
	}

	id := ids.NewV7()
	key := blobstore.WorkspaceKey(workspaceID(ctx), "attachment", id.String())
	checksum, size, err := digest(in.Content)
	if err != nil {
		return crmcontracts.Attachment{}, err
	}

	if err := s.blob.Put(ctx, key, in.Content, size, in.ContentType); err != nil {
		return crmcontracts.Attachment{}, err
	}

	var out crmcontracts.Attachment
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// Re-checked HERE, in the transaction that writes the column. The
		// pre-flight check above runs before the bytes are stored, so an
		// agreement archived or re-anchored during the upload would otherwise
		// still receive the document — the row commits against a contract the
		// caller can no longer see.
		if err := ensureContractFileable(ctx, tx, in.ContractID); err != nil {
			return err
		}
		rollUp, hasAccount, err := accountRollUp(ctx, tx, in.EntityType, in.EntityID)
		if err != nil {
			return err
		}
		var account *ids.UUID
		if hasAccount {
			account = &rollUp
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO attachment (id, entity_type, entity_id, filename,
				content_type, byte_size, storage_key, checksum, source, captured_by,
				organization_id, contract_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, in.EntityType, in.EntityID, in.Filename,
			nullIfEmpty(in.ContentType), size, key, checksum, attachmentSource, by,
			account, in.ContractID); err != nil {
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
