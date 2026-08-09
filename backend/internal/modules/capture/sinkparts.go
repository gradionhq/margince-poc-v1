// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The files a captured message carried, handed to the module that owns them.
//
// Capture never writes the attachment table. It owns the transaction and the
// provenance; the timeline module owns the row shape, the account roll-up and
// the idempotency key, exactly as it does for a human upload. This is the same
// arrangement capture already has with people for counterparties — the seam is
// how capture stays out of a sibling's tables.
//
// Nothing here decides bounds, filenames or content types either. Those are
// settled by the one mail parser every adapter shares, before a part reaches
// the seam: a bound enforced twice is two bounds that can disagree.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// actionPartsDropped is the breadcrumb a bounded message leaves. It carries the
// reason and the natural key and nothing a sender wrote — no filename, no type,
// no size — because system_log is operational and a sender controls all three.
const actionPartsDropped = "capture_parts_dropped"

// fieldPartOrdinal names WHICH part was refused, so two drops on one message
// are two distinguishable facts rather than one line repeated.
const fieldPartOrdinal = "part_ordinal"

// FileKeeper is the timeline module's attachment writer, injected at
// composition so capture never imports it.
//
// Two calls rather than one because the two halves must straddle the
// transaction: bytes are durable BEFORE the row that points at them exists, and
// only the row can join the capture transaction.
type FileKeeper interface {
	// Stage writes each file's bytes to object storage.
	Stage(ctx context.Context, workspace ids.WorkspaceID, files []CapturedFile) ([]StagedFile, error)
	// Record writes the rows, inside the transaction that captured the message.
	Record(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
		from FileSource, staged []StagedFile) error
}

// CapturedFile is one file a message carried, already bounded, renamed safely
// and typed by its bytes.
type CapturedFile struct {
	PartID       string
	Filename     string
	ContentType  string
	DeclaredType string
	Body         []byte
}

// StagedFile is one file whose bytes are already durable. Opaque here: capture
// carries it from Stage to Record and never reads inside it.
type StagedFile any

// FileSource is the provenance every file from one message shares.
type FileSource struct {
	System     string
	MessageID  string
	CapturedBy string
}

// stageParts hands the message's files to the keeper for storage.
//
// A deployment with no keeper wired keeps the MESSAGE and no files rather than
// failing the capture: correspondence is what the timeline exists for, and
// refusing it over an unconfigured object store would lose a real exchange.
func (s *Sink) stageParts(ctx context.Context, rec connector.NormalizedRecord) ([]StagedFile, error) {
	if len(rec.Parts) == 0 || s.files == nil {
		return nil, nil
	}
	files := make([]CapturedFile, 0, len(rec.Parts))
	for _, part := range rec.Parts {
		files = append(files, CapturedFile{
			PartID:       partIdentity(part.Ordinal),
			Filename:     part.Filename,
			ContentType:  part.ContentType,
			DeclaredType: part.DeclaredType,
			Body:         part.Body,
		})
	}
	workspace := ids.From[ids.WorkspaceKind](storekit.MustWorkspace(ctx))
	staged, err := s.files.Stage(ctx, workspace, files)
	if err != nil {
		return nil, fmt.Errorf("capture: storing the files a message carried: %w", err)
	}
	return staged, nil
}

// recordParts writes the rows for one newly captured activity.
func (s *Sink) recordParts(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
	rec connector.NormalizedRecord, staged []StagedFile,
) error {
	if len(staged) == 0 || s.files == nil {
		return nil
	}
	from := FileSource{
		System:     rec.NaturalKey.SourceSystem,
		MessageID:  rec.NaturalKey.SourceID,
		CapturedBy: rec.CapturedBy,
	}
	if err := s.files.Record(ctx, tx, activityID, from, staged); err != nil {
		return fmt.Errorf("capture: recording the files a message carried: %w", err)
	}
	return nil
}

// partIdentity is the part's identity within its message, as stored. It is the
// ordinal rendered as text because the column is text, and because a provider
// that later supplies its own part id can then be told apart from a position we
// counted ourselves.
func partIdentity(ordinal int) string {
	return fmt.Sprintf("part:%d", ordinal)
}

// logPartDrops records what the bounds refused, so a message whose files were
// dropped is distinguishable from one that carried none (DOC-AC-12).
func (s *Sink) logPartDrops(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) error {
	for _, drop := range rec.PartDrops {
		if err := s.logBreadcrumbTx(ctx, tx, actionPartsDropped, rec, drop.Reason,
			map[string]any{fieldPartOrdinal: drop.Ordinal}); err != nil {
			return err
		}
	}
	return nil
}
