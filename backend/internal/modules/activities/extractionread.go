// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The run record for reading one attached document for the deal facts it
// states (RD-DDL-4), and the store methods that move one through its life.
//
// A model call takes seconds and can fail, so it cannot run inside the request
// that asks for it: the POST answers 202 with a reading id and the client polls
// this row until it is terminal. transcriptread.go is the same shape for the
// same reason, and this mirrors it deliberately rather than inventing a second
// vocabulary for one idea.
//
// The one place it does NOT mirror the transcript reading is that this row
// stores its result. A transcript reading produces approval rows that are their
// own authority object (ADR-0036); a document reading produces nothing durable
// but this, and the accept validates a human's choice against exactly the
// fields they were shown (RD-AC-N-5).
//
// This module owns the row; it does not own the reading. The engine that calls
// the model lives in compose, because a module never imports the ai module —
// compose claims the row, reports on it, and finishes it through the methods
// here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// ExtractionReadLease is how long a claimed reading may go unfinished before it
// is treated as abandoned. It exceeds the job's own wall by the terminal
// write's headroom, so a worker that is merely slow to close a reading never
// has it taken away mid-commit.
//
// One number, read by both halves: the worker claims with it and the door
// re-arms with it. Two numbers here would let a reading be reclaimable by one
// and not the other, which is the state nothing can get out of.
const ExtractionReadLease = 5 * time.Minute

// Extraction reading statuses. Queued and running are live; done and failed are
// terminal. Done with no fields is a CORRECT answer — a document that states
// none of them — and is why it is not folded into failed.
const (
	ExtractionReadQueued  = "queued"
	ExtractionReadRunning = "running"
	ExtractionReadDone    = "done"
	ExtractionReadFailed  = "failed"
)

// ExtractionRead is one reading of one attached document.
type ExtractionRead struct {
	ID           ids.UUID
	AttachmentID ids.UUID
	Status       string
	StatusDetail *string
	// Fields is what the reading grounded and what it honestly omitted. It is
	// the reading's OWN answer, not a fresh one: an accept resolves the value it
	// writes from here, so a later reading of the same document cannot change
	// what a human already agreed to.
	Fields      []extraction.ExtractedField
	RequestedBy string
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
}

// Live reports whether the reading is still expected to move on its own — the
// one question the polling client actually asks.
func (r ExtractionRead) Live() bool {
	return r.Status == ExtractionReadQueued || r.Status == ExtractionReadRunning
}

const extractionReadColumns = `id, attachment_id, status, status_detail, fields,
	requested_by, started_at, finished_at, created_at`

func scanExtractionRead(r pgx.Row) (ExtractionRead, error) {
	var read ExtractionRead
	var raw []byte
	err := r.Scan(&read.ID, &read.AttachmentID, &read.Status, &read.StatusDetail, &raw,
		&read.RequestedBy, &read.StartedAt, &read.FinishedAt, &read.CreatedAt)
	if err != nil {
		return read, err
	}
	if err := json.Unmarshal(raw, &read.Fields); err != nil {
		return ExtractionRead{}, fmt.Errorf("decode extraction reading fields: %w", err)
	}
	return read, nil
}

// ExtractionReadEnqueue hands the reading to a worker inside the SAME
// transaction that creates the row, so no queued reading can exist with no work
// behind it — and no job can reference a row that rolled back.
type ExtractionReadEnqueue func(ctx context.Context, tx pgx.Tx, read ExtractionRead) error

// StartExtractionReadQueued creates the queued reading of an attachment, or
// JOINS the one already in flight — pressing the button twice attaches the
// caller to the running reading rather than paying for the same document twice.
// joined reports which happened.
//
// Row-scoped through the attachment's own parent gate: a document the caller
// cannot see answers ErrNotFound, existence-hiding, exactly as every other
// attachment operation does. The scan gate refuses a scanning or blocked row
// here, at the door — before any bytes could reach a model, and before a
// reading exists to explain itself later.
func (s *Store) StartExtractionReadQueued(
	ctx context.Context, attachmentID ids.UUID, requestedBy string, enqueue ExtractionReadEnqueue,
) (ExtractionRead, bool, error) {
	var out ExtractionRead
	var joined bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Update, not read: what a reading exists to produce is a change to the
		// deal. A caller who may read a document but not write what it says has
		// nothing to gain from a reading whose every outcome they could not accept.
		if _, err := resolveVisibleAttachmentParent(ctx, tx, attachmentID, principal.ActionUpdate); err != nil {
			return err
		}
		att, err := readAttachment(ctx, tx, attachmentID)
		if err != nil {
			return err
		}
		if err := EnsureAttachmentScanClean(att.ScanStatus); err != nil {
			return err
		}
		readID := ids.NewV7()
		// In-flight uniqueness is arbitrated by uq_attachment_extraction_inflight
		// itself: DO NOTHING rather than catching the violation keeps the
		// transaction alive, so the join SELECT below sees the winning row in the
		// same tx, with no second-transaction gap for it to finish in.
		inserted := tx.QueryRow(ctx, `
			INSERT INTO attachment_extraction (id, attachment_id, requested_by)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING
			RETURNING `+extractionReadColumns,
			readID, attachmentID, requestedBy)
		out, err = scanExtractionRead(inserted)
		if err == nil {
			if enqueue != nil {
				if err := enqueue(ctx, tx, out); err != nil {
					return err
				}
			}
			// Audit-only: the closed catalog (events.md §5) defines no
			// attachment_extraction.* type. What a reading produces reaches a
			// record only through the accept, which emits the deal's own event.
			if _, err := storekit.Audit(ctx, tx, "create", "attachment_extraction", readID, nil, map[string]any{
				"attachment_id": attachmentID.String(), "requested_by": requestedBy,
			}); err != nil {
				return fmt.Errorf("audit extraction reading start: %w", err)
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("start extraction reading: %w", err)
		}
		joined = true
		out, err = scanExtractionRead(tx.QueryRow(ctx, `
			SELECT `+extractionReadColumns+`
			  FROM attachment_extraction
			 WHERE attachment_id = $1 AND status IN ('queued','running')`, attachmentID))
		if errors.Is(err, pgx.ErrNoRows) {
			// The reading finished between the insert's conflict and this select.
			// Nothing is wrong and nothing is in flight — saying so beats a 500 on
			// an innocent second press.
			return fmt.Errorf("%w: the previous reading of this document finished as this one was starting; ask again",
				apperrors.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("join in-flight extraction reading: %w", err)
		}
		return s.rearmIfAbandonedExtraction(ctx, tx, &out, enqueue)
	})
	return out, joined, err
}

// rearmIfAbandonedExtraction hands a dead reading back to a worker.
//
// A row still running past its lease is not a live reading: the worker that
// claimed it was killed, timed out, or exhausted its retries. Nothing else
// would ever pick it up — a finished job is not re-enqueued, and
// uq_attachment_extraction_inflight makes the corpse block every new reading of
// that document — so without this the document is unreadable for good.
//
// Pressing the button again is therefore the recovery path, which is also the
// thing a rep would try unprompted.
func (s *Store) rearmIfAbandonedExtraction(
	ctx context.Context, tx pgx.Tx, read *ExtractionRead, enqueue ExtractionReadEnqueue,
) error {
	if read.Status != ExtractionReadRunning {
		return nil
	}
	rearmed, err := scanExtractionRead(tx.QueryRow(ctx, `
		UPDATE attachment_extraction
		   SET status = 'queued', started_at = NULL, status_detail = NULL
		 WHERE id = $1
		   AND status = 'running'
		   AND started_at < now() - ($2 * interval '1 microsecond')
		RETURNING `+extractionReadColumns, read.ID, ExtractionReadLease.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		// Inside its lease: a real worker holds it, and joining is correct.
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-arm abandoned extraction reading: %w", err)
	}
	*read = rearmed
	if enqueue == nil {
		return nil
	}
	return enqueue(ctx, tx, rearmed)
}

// BeginExtractionRead claims a queued reading, moving it to running.
//
// The compare-and-set is the claim, and it has TWO arms because a second
// delivery of the same job means two different things. A live holder is inside
// its lease and must be left alone — reading the document twice bills it twice.
// A holder past its lease is a dead attempt: the worker was killed, timed out,
// or the process went away mid-model-call, and the row it left behind is
// running with nobody working it.
//
// Without the second arm every retry after a transient provider failure finds
// the row already running, declines it as somebody else's, and returns. The
// reading is then stranded running forever — and because the in-flight index
// counts running as in flight, the document becomes permanently unreadable.
func (s *Store) BeginExtractionRead(ctx context.Context, readID ids.UUID, reclaimAfter time.Duration) (ExtractionRead, error) {
	if reclaimAfter <= 0 {
		return ExtractionRead{}, errors.New("activities: the extraction-reading reclaim interval must be positive")
	}
	var out ExtractionRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := requireExtractionAuthority(ctx, tx, readID); err != nil {
			return err
		}
		var err error
		// RETURNING hands the worker the CLAIMED row's own identity, so the
		// reading is attributed to whoever the row says asked for it rather than
		// to a job payload that could in principle disagree with it.
		out, err = scanExtractionRead(tx.QueryRow(ctx, `
			UPDATE attachment_extraction
			   SET status = 'running', status_detail = NULL, started_at = now()
			 WHERE id = $1
			   AND (status = 'queued'
			     OR (status = 'running' AND started_at < now() - ($2 * interval '1 microsecond')))
			RETURNING `+extractionReadColumns, readID, reclaimAfter.Microseconds()))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: extraction reading %s is not claimable", apperrors.ErrConflict, readID)
		}
		if err != nil {
			return fmt.Errorf("claim extraction reading: %w", err)
		}
		return nil
	})
	return out, err
}

// ExtractionReadOutcome is what a finished reading has to report.
type ExtractionReadOutcome struct {
	// Status is done or failed. Done with no grounded field is the honest answer
	// for a document that states none of them, and Detail says so.
	Status string
	// Detail explains the outcome in words a rep can act on. Required for
	// failed, and for a done reading that grounded nothing — an empty result
	// that does not explain itself reads as a broken feature.
	Detail string
	// Fields is everything the reading produced: the grounded fields and the
	// omissions alike. Both are stored, because an omission is an answer the
	// panel renders, not an absence.
	Fields []extraction.ExtractedField
}

// FinishExtractionRead records what the reading produced and closes it.
func (s *Store) FinishExtractionRead(ctx context.Context, readID ids.UUID, outcome ExtractionReadOutcome) error {
	if outcome.Status != ExtractionReadDone && outcome.Status != ExtractionReadFailed {
		return fmt.Errorf("activities: an extraction reading finishes done or failed, not %q", outcome.Status)
	}
	if outcome.Detail == "" && (outcome.Status == ExtractionReadFailed || !anyGrounded(outcome.Fields)) {
		return errors.New(
			"activities: a failed or ungrounded extraction reading must say why, or its result cannot be told from a broken one")
	}
	encoded, err := json.Marshal(nonNilFields(outcome.Fields))
	if err != nil {
		return fmt.Errorf("encode extraction reading fields: %w", err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := requireExtractionAuthority(ctx, tx, readID); err != nil {
			return err
		}
		var detail *string
		if outcome.Detail != "" {
			detail = &outcome.Detail
		}
		tag, err := tx.Exec(ctx, `
			UPDATE attachment_extraction
			   SET status = $2, status_detail = $3, fields = $4, finished_at = now()
			 WHERE id = $1 AND status = 'running'`,
			readID, outcome.Status, detail, encoded)
		if err != nil {
			return fmt.Errorf("finish extraction reading: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: extraction reading %s is not running", apperrors.ErrConflict, readID)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "attachment_extraction", readID, nil, map[string]any{
			"status": outcome.Status, "grounded": groundedCount(outcome.Fields),
		}); err != nil {
			return fmt.Errorf("audit extraction reading finish: %w", err)
		}
		return nil
	})
}

// requireExtractionAuthority gates a worker-side entry point through the SAME
// parent walk the door took: the reading's attachment must still resolve under
// the acting principal, with the authority to change what the document is
// about.
//
// Attachments carry no RBAC object of their own — authority over one is
// authority over the record it hangs off — so the gate cannot be a bare
// auth.Require here and has to reach the row first. Doing it inside the claim
// is what makes a reading of a record whose access was revoked between the
// request and the worker picking it up stop, rather than finish and leave
// grounded values sitting on a row nobody may see.
//
// A reading whose row is gone answers ErrNotFound, which is also what a caller
// who may not see it gets — existence-hiding holds here as everywhere else.
func requireExtractionAuthority(ctx context.Context, tx pgx.Tx, readID ids.UUID) error {
	var attachmentID ids.UUID
	err := tx.QueryRow(ctx, `SELECT attachment_id FROM attachment_extraction WHERE id = $1`, readID).Scan(&attachmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: extraction reading %s", apperrors.ErrNotFound, readID)
	}
	if err != nil {
		return fmt.Errorf("resolve extraction reading: %w", err)
	}
	_, err = resolveVisibleAttachmentParent(ctx, tx, attachmentID, principal.ActionUpdate)
	return err
}

// anyGrounded reports whether the reading produced a value a human could
// actually accept. A reading that returned only omissions has grounded nothing,
// however many rows it carries — which is why the count, not the length, is
// what decides whether a detail is owed.
func anyGrounded(fields []extraction.ExtractedField) bool { return groundedCount(fields) > 0 }

func groundedCount(fields []extraction.ExtractedField) int {
	n := 0
	for _, f := range fields {
		if !f.Omitted {
			n++
		}
	}
	return n
}

// nonNilFields keeps the stored value a JSON array even when a reading produced
// nothing, so the CHECK holds and every reader decodes one shape.
func nonNilFields(fields []extraction.ExtractedField) []extraction.ExtractedField {
	if fields == nil {
		return []extraction.ExtractedField{}
	}
	return fields
}

// GetExtractionRead resolves ONE reading by id, refusing one that belongs to a
// different document.
//
// The accept path uses this rather than LatestExtractionRead, and the pairing
// is the whole guarantee: a human accepts the reading they were shown, named by
// its id, so a reading somebody else started between the display and the click
// cannot decide what gets written (RD-AC-N-5). Resolving "the newest" there
// would reintroduce the divergence storing the fields exists to prevent.
//
// Row-scoped like every other read: a reading of a document the caller cannot
// see does not exist, and neither does one under another document.
func (s *Store) GetExtractionRead(ctx context.Context, attachmentID, readID ids.UUID) (ExtractionRead, error) {
	var out ExtractionRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := resolveVisibleAttachmentParent(ctx, tx, attachmentID, principal.ActionRead); err != nil {
			return err
		}
		var err error
		out, err = scanExtractionRead(tx.QueryRow(ctx, `
			SELECT `+extractionReadColumns+`
			  FROM attachment_extraction
			 WHERE id = $1 AND attachment_id = $2`, readID, attachmentID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: extraction reading %s", apperrors.ErrNotFound, readID)
		}
		if err != nil {
			return fmt.Errorf("read extraction reading: %w", err)
		}
		return nil
	})
	return out, err
}

// LatestExtractionRead answers "has this document been read, and how did it go".
// ErrNotFound means never read, which the client renders as the offer to read
// it — the honest difference between nobody asking and a reading that got
// nothing.
//
// A read of a record, so it carries the row-scope gate like every other one: a
// reading of a document the caller cannot see does not exist.
func (s *Store) LatestExtractionRead(ctx context.Context, attachmentID ids.UUID) (ExtractionRead, error) {
	var out ExtractionRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := resolveVisibleAttachmentParent(ctx, tx, attachmentID, principal.ActionRead); err != nil {
			return err
		}
		var err error
		out, err = scanExtractionRead(tx.QueryRow(ctx, `
			SELECT `+extractionReadColumns+`
			  FROM attachment_extraction
			 WHERE attachment_id = $1
			 ORDER BY created_at DESC
			 LIMIT 1`, attachmentID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no extraction reading for attachment %s", apperrors.ErrNotFound, attachmentID)
		}
		if err != nil {
			return fmt.Errorf("read latest extraction reading: %w", err)
		}
		return nil
	})
	return out, err
}
