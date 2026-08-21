// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The WORKER's half of a document reading: claiming one, closing it, and
// handing it back.
//
// Split from extractionread.go, which keeps the door's half — starting a
// reading, joining one already in flight, re-arming an abandoned one, and
// reading the result back. The two halves are reached by different callers
// under different principals, and only this one is scoped to a single CLAIM:
// every statement here closes on the started_at the claim returned, so a worker
// whose lease expired mid-model-call cannot overwrite the live worker's answer
// with its own stale one.

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
		// The RECLAIM arm begins a new attempt; the ordinary arm does not.
		//
		// Both are one statement, so the CASE reads the row's OLD status — and
		// that distinction is load-bearing. Taking a queued reading is the
		// attempt that was already queued. Taking one away from a dead holder is
		// a SECOND claim on the same attempt number, and a projection ordering
		// on (attempt, state) cannot tell that from a redelivery of the first:
		// it would keep the dead worker's started_at and its expired lease, and
		// render an actively-running retry as stalled for its whole run.
		out, err = scanExtractionRead(tx.QueryRow(ctx, `
			UPDATE attachment_extraction
			   SET status = 'running', status_detail = NULL, started_at = now(),
			       attempt    = attempt + CASE WHEN status = 'running' THEN 1 ELSE 0 END,
			       attempt_at = CASE WHEN status = 'running' THEN now() ELSE attempt_at END
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
		return logExtractionActivity(ctx, tx, out)
	})
	return out, err
}

// ExtractionReadOutcome is what a finished reading has to report.
type ExtractionReadOutcome struct {
	// ClaimedAt is the start time of the claim this outcome belongs to, taken
	// from the row BeginExtractionRead returned. It is what makes the finish
	// specific to one attempt: a worker whose lease expired mid-model-call has
	// already had the reading taken from it, and closing on `status = running`
	// alone would let it overwrite the live worker's answer with its own stale
	// one — both writes legitimate-looking, the later reading silently lost.
	ClaimedAt time.Time
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
		// RETURNING rather than a row count: the same statement has to answer
		// both "did this claim still own the reading" and "what does the closed
		// row now say", and a second SELECT for the latter could read a row a
		// rival transaction had already moved on.
		closed, err := scanExtractionRead(tx.QueryRow(ctx, `
			UPDATE attachment_extraction
			   SET status = $2, status_detail = $3, fields = $4, finished_at = now()
			 WHERE id = $1 AND status = 'running' AND started_at = $5
			RETURNING `+extractionReadColumns,
			readID, outcome.Status, detail, encoded, outcome.ClaimedAt))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: extraction reading %s is not running under this claim", apperrors.ErrConflict, readID)
		}
		if err != nil {
			return fmt.Errorf("finish extraction reading: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "attachment_extraction", readID, nil, map[string]any{
			"status": outcome.Status, "grounded": groundedCount(outcome.Fields),
		})
		if err != nil {
			return fmt.Errorf("audit extraction reading finish: %w", err)
		}
		return emitExtractionActivity(ctx, tx, auditID, closed)
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
	_, err = resolveAttachmentParent(ctx, tx, attachmentID, principal.ActionUpdate)
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

// ReleaseExtractionRead hands a claimed reading back to the queue.
//
// A worker that is about to return a retryable error must return the reading
// with it. Without that the row stays `running` while the job retries, its own
// re-claim is refused as somebody else's live lease, and the retry reports
// success — leaving a reading that no worker holds, no job will pick up, and no
// surface offers a way out of, because a live reading renders as "reading…"
// with nothing to press.
//
// The lease still covers the ungraceful case (a killed process cannot release
// anything); this covers the ordinary one, which is far more common.
func (s *Store) ReleaseExtractionRead(ctx context.Context, readID ids.UUID, claimedAt time.Time) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := requireExtractionAuthority(ctx, tx, readID); err != nil {
			return err
		}
		// Scoped to this claim, like the finish: a worker whose lease already
		// expired must not re-queue the reading somebody else is now working.
		released, err := scanExtractionRead(tx.QueryRow(ctx, `
			UPDATE attachment_extraction
			   SET status = 'queued', started_at = NULL, status_detail = NULL,
			       attempt = attempt + 1, attempt_at = now()
			 WHERE id = $1 AND status = 'running' AND started_at = $2
			RETURNING `+extractionReadColumns, readID, claimedAt))
		if errors.Is(err, pgx.ErrNoRows) {
			// The claim is no longer this worker's — somebody else holds the
			// reading, or it is already closed. Nothing was released, so nothing
			// is announced. Not an error, exactly as before: a worker returning a
			// reading it has already lost has done everything it could.
			return nil
		}
		if err != nil {
			return fmt.Errorf("release extraction reading: %w", err)
		}
		return logExtractionActivity(ctx, tx, released)
	})
}
