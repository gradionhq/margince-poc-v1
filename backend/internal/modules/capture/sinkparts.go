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
// Nothing here decides bounds, filenames or content types, and the two halves of
// that have different reasons. Bounds belong to whichever producer reads the
// bytes, because bounding is what lets it read them at all — today that is the
// one mail parser every adapter shares. Filenames and content types belong to
// the ONE published pair every producer calls (extension.SafeFilename,
// extension.SniffContentType), so no producer can certify the field whose whole
// purpose is to distrust it. Either way a bound enforced twice is two bounds
// that can disagree.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// actionPartsDropped is the breadcrumb a bounded message leaves. It carries the
// reason and the natural key and nothing a sender wrote — no filename, no type,
// no size — because system_log is operational and a sender controls all three.
const actionPartsDropped = "capture_parts_dropped"

// fieldDroppedParts is HOW MANY files one bound refused. A count rather than a
// row each: the number of refusals is the sender's choice, and one breadcrumb
// per refused part would let an inbound message size our own log.
const fieldDroppedParts = "dropped_parts"

// FileKeeper is the timeline module's attachment writer, injected at
// composition so capture never imports it.
//
// Two calls rather than one because the two halves must straddle the
// transaction: bytes are durable BEFORE the row that points at them exists, and
// only the row can join the capture transaction.
type FileKeeper interface {
	// Stage writes each file's bytes to object storage.
	Stage(ctx context.Context, files []CapturedFile) ([]StagedFile, error)
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

// StagedFile is one file whose bytes are already durable.
//
// A marker interface rather than `any`: capture must not read inside the value,
// and this way the COMPILER says so. Only the owning module can satisfy it, so
// there is no runtime assertion and no unreachable branch for a value that
// could not have come from anywhere else.
type StagedFile interface {
	// StagedFile reports that this value came from the module that owns the
	// attachment table. It carries nothing: the point is that capture can hold
	// the value and cannot open it.
	StagedFile()
}

// FileSource is the provenance every file from one message shares.
type FileSource struct {
	System     string
	MessageID  string
	CapturedBy string
	// Category is which kind of captured file these are, derived from the
	// record's own transport by fileCategoryFor — never taken from a connector.
	Category string
}

// The categories a CAPTURED file can have. A human upload picks from the part of
// the vocabulary that describes what a document IS (contract, offer, legal);
// these describe how it ARRIVED, which is the only thing a captured file knows
// about itself.
const (
	CategoryEmailAttachment   = "email_attachment"
	CategoryMessageAttachment = "message_attachment"
	// CategoryOtherAttachment is a captured file that arrived on neither — see
	// fileCategoryFor's third arm.
	CategoryOtherAttachment = "other"
)

// kindEmail is the activity kind a mail capture files under. Named here because
// this is the one place the kind decides a durable column's value, and a string
// literal in that position reads as incidental.
const kindEmail = "email"

// fileCategoryFor derives the category from the record's own transport.
//
// DERIVED, never supplied. A connector-declared category would be a string an
// untrusted producer could get wrong on a column that reaches the document
// library, the audit image and every category filter — and the record already
// names how it arrived, so there is nothing to ask for.
//
// THREE ARMS, BECAUSE "NOT A CHANNEL" DOES NOT MEAN "MAIL". A calendar capture
// files a `meeting` with no transport (capture/gcal), and the offline demo
// connector files whatever kind its fixture names — so an absent provider read
// as mail would label a meeting's file an email attachment the moment either
// producer starts carrying files. `other` is the honest answer for a captured
// file that arrived on neither: the contract calls it "the honest default, not a
// fallback for an unknown value", and this is exactly the case it describes.
//
// The alternative — refusing the file — was rejected. The refusal would fail the
// whole capture, and losing the message to protect the precision of a label is
// the worse trade on a path whose purpose is that correspondence lands.
func fileCategoryFor(fields ActivityFields) string {
	switch {
	case fields.ChannelProvider != "":
		return CategoryMessageAttachment
	case fields.Kind == kindEmail:
		return CategoryEmailAttachment
	default:
		return CategoryOtherAttachment
	}
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
	// Proven, never assumed. Every other write here names
	// current_setting('app.workspace_id') in its own predicate, so an unbound
	// GUC makes it write NULL and fail its NOT NULL. An object store has no
	// such column to fail on: an unbound context would write real attachment
	// bytes under the zero workspace, outside any tenant's reach and outside
	// erasure's.
	workspace, ok := principal.WorkspaceID(ctx)
	if !ok || workspace == (ids.UUID{}) {
		return nil, fmt.Errorf(
			"capture: a message's files cannot be stored without a bound workspace: %w",
			apperrors.ErrPermissionDenied)
	}
	staged, err := s.files.Stage(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("capture: storing the files a message carried: %w", err)
	}
	return staged, nil
}

// recordParts writes the rows for one newly captured activity.
//
// It takes the typed fields rather than reading them back out of rec.Fields:
// the caller already holds them, and re-asserting the type here would be a
// second place that has to agree about which records carry files.
func (s *Sink) recordParts(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
	rec connector.NormalizedRecord, fields ActivityFields, staged []StagedFile,
) error {
	if len(staged) == 0 || s.files == nil {
		return nil
	}
	from := FileSource{
		System:     rec.NaturalKey.SourceSystem,
		MessageID:  rec.NaturalKey.SourceID,
		CapturedBy: rec.CapturedBy,
		Category:   fileCategoryFor(fields),
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
			map[string]any{fieldDroppedParts: drop.Count}); err != nil {
			return err
		}
	}
	return nil
}
