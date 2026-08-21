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
//
// This file is the DOOR's half: starting a reading, joining one already in
// flight, re-arming an abandoned one, and reading the result back. The worker's
// half — claim, finish, release — is extractionclaim.go, because those three
// are the ones scoped to a single claim and reached under a different
// principal.

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
	// Attempt is which claim of this reading is current, and AttemptAt is when
	// it became current. Attempt rises whenever a live claim is superseded — a
	// release, a re-arm, or a lease-expiry reclaim — because each of those makes
	// what follows a NEW attempt at the same reading, and the AI-activity
	// projection cannot order two events for one reading on status alone.
	//
	// AttemptAt is what a live occurrence AGES from. created_at is that instant
	// for the first attempt only: a reading re-queued an hour later and dated by
	// created_at is past its lease before any worker sees it.
	Attempt   int
	AttemptAt time.Time
}

// Live reports whether the reading is still expected to move on its own — the
// one question the polling client actually asks.
func (r ExtractionRead) Live() bool {
	return r.Status == ExtractionReadQueued || r.Status == ExtractionReadRunning
}

const extractionReadColumns = `id, attachment_id, status, status_detail, fields,
	requested_by, started_at, finished_at, created_at, attempt, attempt_at`

func scanExtractionRead(r pgx.Row) (ExtractionRead, error) {
	var read ExtractionRead
	var raw []byte
	err := r.Scan(&read.ID, &read.AttachmentID, &read.Status, &read.StatusDetail, &raw,
		&read.RequestedBy, &read.StartedAt, &read.FinishedAt, &read.CreatedAt, &read.Attempt, &read.AttemptAt)
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
// attachment operation does. That gate runs at the door — before any bytes
// could reach a model, and before a reading exists to explain itself later.
func (s *Store) StartExtractionReadQueued(
	ctx context.Context, attachmentID ids.UUID, requestedBy string, enqueue ExtractionReadEnqueue,
) (ExtractionRead, bool, error) {
	var out ExtractionRead
	var joined bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Update, not read: what a reading exists to produce is a change to the
		// deal. A caller who may read a document but not write what it says has
		// nothing to gain from a reading whose every outcome they could not accept.
		if _, err := resolveAttachmentParent(ctx, tx, attachmentID, principal.ActionUpdate); err != nil {
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
		// `out` is the closure's, not this block's, so the pair is assigned
		// rather than declared — `:=` here would shadow it and the caller would
		// receive a zero reading.
		var err error
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
			auditID, err := storekit.Audit(ctx, tx, "create", "attachment_extraction", readID, nil, map[string]any{
				"attachment_id": attachmentID.String(), "requested_by": requestedBy,
			})
			if err != nil {
				return fmt.Errorf("audit extraction reading start: %w", err)
			}
			return emitExtractionActivity(ctx, tx, auditID, out)
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
	// Both live statuses can strand, and only one of them is obvious. A RUNNING
	// row past its lease is the killed worker. A QUEUED row past it is a job
	// that never claimed at all — cancelled, discarded after exhausting its
	// attempts, or lost with the queue — and it strands exactly as hard, because
	// the in-flight index makes the corpse block every new reading of the
	// document while `rearm` is the only thing that could clear it.
	rearmed, err := scanExtractionRead(tx.QueryRow(ctx, `
		UPDATE attachment_extraction
		   SET status = 'queued', started_at = NULL, status_detail = NULL,
		       attempt = attempt + 1, attempt_at = now()
		 WHERE id = $1
		   AND status IN ('queued','running')
		   AND COALESCE(started_at, created_at) < now() - ($2 * interval '1 microsecond')
		RETURNING `+extractionReadColumns, read.ID, ExtractionReadLease.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		// Inside its lease: a real worker holds it, and joining is correct.
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-arm abandoned extraction reading: %w", err)
	}
	*read = rearmed
	if err := logExtractionActivity(ctx, tx, rearmed); err != nil {
		return err
	}
	if enqueue == nil {
		return nil
	}
	return enqueue(ctx, tx, rearmed)
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
		if _, err := resolveAttachmentParent(ctx, tx, attachmentID, principal.ActionRead); err != nil {
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
		if _, err := resolveAttachmentParent(ctx, tx, attachmentID, principal.ActionRead); err != nil {
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
