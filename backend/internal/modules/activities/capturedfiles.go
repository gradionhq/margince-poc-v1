// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The files a captured message carried, becoming attachment rows.
//
// This lives in the module that OWNS the attachment table rather than in the
// one that reads the mailbox. Capture drives the transaction and supplies the
// files; the row shape, the account roll-up and the idempotency key are this
// module's, exactly as they are for a human upload. Two writers of one table
// with two spellings of its invariants is how the two come to disagree.
//
// ORDER IS THE DESIGN. The bytes go to object storage BEFORE the transaction
// that records them, the same order UploadAttachment uses and for the same
// reason: the worst outcome of a failure between the two is an object nobody
// references, and the alternative is a row promising bytes that are not there.
// One is invisible and reclaimable; the other is a broken download on a surface
// whose whole promise is that a cited file opens. Reclaiming the orphan is
// issue #663 and is owed by both writers, not only this one.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CapturedFile is one file that arrived with a message, already bounded,
// renamed safely and typed by its bytes.
//
// Nothing here re-decides those three: they are settled by the one mail parser
// every adapter shares, and a second opinion at this end is how two adapters
// come to disagree about how large a file may be.
type CapturedFile struct {
	// PartID is the file's identity within its provider message. With the
	// message id it is what capture is idempotent on.
	PartID       string
	Filename     string
	ContentType  string
	DeclaredType string
	Body         []byte
}

// StagedFile is one captured file whose bytes are already durable, waiting for
// the row that will point at them.
type StagedFile struct {
	file     CapturedFile
	id       ids.UUID
	key      string
	checksum string
}

// StageCapturedFiles writes each file's bytes to object storage.
//
// A deployment with no blob seam keeps the MESSAGE and no files rather than
// failing the capture: correspondence is what the timeline exists for, and
// refusing it over an unconfigured store would lose a real exchange.
func (s *Store) StageCapturedFiles(
	ctx context.Context, workspace ids.WorkspaceID, files []CapturedFile,
) ([]StagedFile, error) {
	// Gated even though it writes no row: this is where a caller turns bytes it
	// supplied into durable objects, and an entry point that trusts its caller
	// is one refactor away from being reachable by a caller that was never
	// admitted. Create on activity is what the capture principal holds and what
	// this is — a file arriving with a message it is creating.
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return nil, err
	}
	if len(files) == 0 || s.blob == nil {
		return nil, nil
	}
	staged := make([]StagedFile, 0, len(files))
	for _, file := range files {
		id := ids.NewV7()
		key := blobstore.WorkspaceKey(workspace, "attachment", id.String())
		sum := sha256.Sum256(file.Body)
		if err := s.blob.Put(ctx, key, bytes.NewReader(file.Body),
			int64(len(file.Body)), file.ContentType); err != nil {
			return nil, fmt.Errorf("store a captured file: %w", err)
		}
		staged = append(staged, StagedFile{
			file: file, id: id, key: key, checksum: hex.EncodeToString(sum[:]),
		})
	}
	return staged, nil
}

// CapturedFileSource is the provenance every file from one message shares.
type CapturedFileSource struct {
	System     string
	MessageID  string
	CapturedBy string
}

// RecordCapturedFiles writes the attachment rows for one newly captured
// activity, inside the transaction that captured it.
//
// The caller runs this only for an activity its pull actually created, which is
// the message-level idempotency the natural key already gives. The unique index
// on the provider's (message, part) identity is the second lock, and it is what
// holds when two pulls of one mailbox overlap in time (DOC-AC-8).
func (s *Store) RecordCapturedFiles(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
	from CapturedFileSource, staged []StagedFile,
) error {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return err
	}
	if len(staged) == 0 {
		return nil
	}
	account, filed, err := accountForCapturedActivity(ctx, tx, activityID)
	if err != nil {
		return err
	}
	// A message filed against no company writes NULL, which is what keeps the
	// file out of every account library rather than in the wrong one.
	var rollUp *ids.UUID
	if filed {
		rollUp = &account
	}
	for _, file := range staged {
		if err := insertCapturedAttachment(ctx, tx, activityID, rollUp, from, file); err != nil {
			return err
		}
	}
	return nil
}

func insertCapturedAttachment(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, account *ids.UUID,
	from CapturedFileSource, file StagedFile,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO attachment (
			id, workspace_id, entity_type, entity_id, filename, content_type,
			byte_size, storage_key, checksum, source, captured_by,
			category, organization_id, activity_id,
			external_source_id, external_part_id, declared_type)
		VALUES ($1, current_setting('app.workspace_id')::uuid, 'activity', $2, $3, $4,
		        $5, $6, $7, $8, $9,
		        'email_attachment', $10, $2,
		        $11, $12, $13)
		ON CONFLICT (workspace_id, external_source_id, external_part_id)
		  WHERE external_source_id IS NOT NULL DO NOTHING`,
		file.id, activityID, file.file.Filename, nullIfEmpty(file.file.ContentType),
		len(file.file.Body), file.key, file.checksum, from.System, from.CapturedBy,
		account, from.MessageID, file.file.PartID, nullIfEmpty(file.file.DeclaredType))
	if err != nil {
		return fmt.Errorf("record a captured file: %w", err)
	}
	return nil
}

// accountForCapturedActivity resolves the account roll-up for a captured file.
//
// The roll-up exists so a company's document library is one indexed read rather
// than a union over every polymorphic parent. It is NOT what authorizes the
// file — that stays the activity, which owns its own visibility — so a message
// filed against nobody rolls up to nothing, and its file is reachable from the
// timeline alone until a relink gives it an account.
func accountForCapturedActivity(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
) (ids.UUID, bool, error) {
	// A direct company link first, then the company behind a linked deal. The
	// order is the point: a message linked to both names one company either way,
	// and preferring the direct link means the roll-up does not move if the deal
	// is later re-parented.
	var account ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT organization_id FROM (
			SELECT link.organization_id, 0 AS rank
			  FROM activity_link link
			 WHERE link.workspace_id = current_setting('app.workspace_id')::uuid
			   AND link.activity_id = $1 AND link.entity_type = 'organization'
			UNION ALL
			SELECT d.organization_id, 1 AS rank
			  FROM activity_link link
			  JOIN deal d ON d.id = link.deal_id
			 WHERE link.workspace_id = current_setting('app.workspace_id')::uuid
			   AND link.activity_id = $1 AND link.entity_type = 'deal'
			   AND d.organization_id IS NOT NULL
		) candidates
		 ORDER BY rank, organization_id
		 LIMIT 1`, activityID).Scan(&account)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ids.UUID{}, false, nil
		}
		return ids.UUID{}, false, fmt.Errorf("resolve the account a captured file belongs to: %w", err)
	}
	return account, true, nil
}
