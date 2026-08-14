// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The run record for reading a meeting transcript for the next steps in it
// (S-E04.3), and the four store methods that move one through its life.
//
// A model call takes seconds and can fail, so it cannot run inside the request
// that asks for it: the POST answers 202 with a read id and the client polls
// this row until it is terminal. Deep read (people/siteread.go) is the same
// shape for the same reason, and this mirrors it deliberately rather than
// inventing a second vocabulary for the same idea.
//
// This module owns the row; it does not own the reading. The engine that calls
// the model and stages the proposals lives in compose, because a module never
// imports the ai module or a sibling — compose claims the row, reports on it,
// and finishes it through the methods here.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// NotATranscriptError maps to 422: reading for next steps addresses the lines
// of a transcript, so an activity that carries none has nothing to cite. The
// message never echoes the caller's kind — an activity kind reaching here has
// already passed the DB CHECK, but the same rule that keeps TranscriptKindError
// quiet applies to every message a client sees.
type NotATranscriptError struct{ Kind string }

func (e *NotATranscriptError) Error() string {
	return "only an activity logged with source_system=transcript can be read for next steps"
}

// FieldFault names the offending field; the caller's value is left to the
// wire's own field pointer, not interpolated into the message.
func (e *NotATranscriptError) FieldFault() (field, code, message string) {
	return "id", "invalid", e.Error()
}

// Transcript read statuses. Queued and running are live; done and failed are
// terminal. Done with no proposals is a CORRECT answer — a transcript that
// states no next steps — and is why it is not folded into failed.
const (
	TranscriptReadQueued  = "queued"
	TranscriptReadRunning = "running"
	TranscriptReadDone    = "done"
	TranscriptReadFailed  = "failed"
)

// TranscriptRead is one reading of one transcript.
type TranscriptRead struct {
	ID           ids.UUID
	ActivityID   ids.ActivityID
	Status       string
	StatusDetail *string
	LineCount    int
	// ProposalIDs are the approvals this read staged, so the surface can say
	// how many questions are waiting without consulting the inbox. It records
	// what the read produced; the authority is on each approval row (ADR-0036).
	ProposalIDs []ids.UUID
	RequestedBy string
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
}

// Live reports whether the reading is still expected to move on its own — the
// one question the polling client actually asks.
func (r TranscriptRead) Live() bool {
	return r.Status == TranscriptReadQueued || r.Status == TranscriptReadRunning
}

const transcriptReadColumns = `id, activity_id, status, status_detail, line_count,
	proposal_ids, requested_by, started_at, finished_at, created_at`

func scanTranscriptRead(r pgx.Row) (TranscriptRead, error) {
	var read TranscriptRead
	err := r.Scan(&read.ID, &read.ActivityID, &read.Status, &read.StatusDetail, &read.LineCount,
		&read.ProposalIDs, &read.RequestedBy, &read.StartedAt, &read.FinishedAt, &read.CreatedAt)
	return read, err
}

// TranscriptReadEnqueue hands the reading to a worker inside the SAME
// transaction that creates the row, so no queued read can exist with no work
// behind it — and no job can reference a row that rolled back.
type TranscriptReadEnqueue func(ctx context.Context, tx pgx.Tx, read TranscriptRead) error

// StartTranscriptReadQueued creates the queued reading of a transcript, or
// JOINS the one already in flight — pressing the button twice attaches the
// caller to the running read rather than paying for the same transcript twice
// and staging every proposal in duplicate. joined reports which happened.
//
// Row-scoped: a transcript the caller cannot see answers ErrNotFound
// (existence-hiding), and one that is not a transcript at all is refused
// rather than read, because there are no addressable lines to cite.
func (s *Store) StartTranscriptReadQueued(
	ctx context.Context, activityID ids.ActivityID, requestedBy string, enqueue TranscriptReadEnqueue,
) (TranscriptRead, bool, error) {
	// Create, not read: what this act produces is proposed task activities. A
	// caller who may read the timeline but not add to it has nothing to gain
	// from a reading whose every outcome they could not accept.
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return TranscriptRead{}, false, err
	}
	var out TranscriptRead
	var joined bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		lineCount, err := transcriptLineCount(ctx, tx, activityID)
		if err != nil {
			return err
		}
		readID := ids.NewV7()
		// In-flight uniqueness is arbitrated by uq_transcript_read_inflight
		// itself: DO NOTHING rather than catching the violation keeps the
		// transaction alive, so the join SELECT below sees the winning row in
		// the same tx, with no second-transaction gap for it to finish in.
		inserted := tx.QueryRow(ctx, `
			INSERT INTO transcript_read (id, workspace_id, activity_id, requested_by, line_count)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING
			RETURNING `+transcriptReadColumns,
			readID, workspaceID(ctx), activityID, requestedBy, lineCount)
		out, err = scanTranscriptRead(inserted)
		if err == nil {
			if enqueue != nil {
				if err := enqueue(ctx, tx, out); err != nil {
					return err
				}
			}
			// Audit-only: the closed catalog (events.md §5) defines no
			// transcript_read.* type. What the reading produces is staged as
			// approvals, each emitting its own event when it is accepted.
			if _, err := storekit.Audit(ctx, tx, "create", "transcript_read", readID, nil, map[string]any{
				"activity_id": activityID.String(), "requested_by": requestedBy, "line_count": lineCount,
			}); err != nil {
				return fmt.Errorf("audit transcript read start: %w", err)
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("start transcript read: %w", err)
		}
		joined = true
		out, err = scanTranscriptRead(tx.QueryRow(ctx, `
			SELECT `+transcriptReadColumns+`
			  FROM transcript_read
			 WHERE activity_id = $1 AND status IN ('queued','running')`, activityID))
		if err != nil {
			return fmt.Errorf("join in-flight transcript read: %w", err)
		}
		return nil
	})
	return out, joined, err
}

// transcriptLineCount resolves how many addressable lines the transcript has,
// and is the gate that refuses a non-transcript. It reads through
// readActivity so the row scope every other single-row access takes applies
// here too — an activity the caller cannot see is ErrNotFound, not an empty
// transcript.
func transcriptLineCount(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID) (int, error) {
	activity, err := readActivity(ctx, tx, activityID, storekit.LiveOnly)
	if err != nil {
		return 0, err
	}
	if activity.SourceSystem == nil || *activity.SourceSystem != transcriptSourceSystem {
		return 0, &NotATranscriptError{Kind: string(activity.Kind)}
	}
	if activity.Body == nil || *activity.Body == "" {
		return 0, ErrBlankTranscript
	}
	return len(transcriptLines(*activity.Body)), nil
}

// BeginTranscriptRead claims a queued reading, moving it to running. The
// compare-and-set is the claim: a second worker handed the same job by an
// at-least-once queue finds no queued row and stops, rather than reading and
// billing the same transcript twice.
func (s *Store) BeginTranscriptRead(ctx context.Context, readID ids.UUID) (TranscriptRead, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return TranscriptRead{}, err
	}
	var out TranscriptRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanTranscriptRead(tx.QueryRow(ctx, `
			UPDATE transcript_read
			   SET status = 'running', started_at = now()
			 WHERE id = $1 AND status = 'queued'
			RETURNING `+transcriptReadColumns, readID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: transcript read %s is not queued", apperrors.ErrConflict, readID)
		}
		if err != nil {
			return fmt.Errorf("claim transcript read: %w", err)
		}
		return nil
	})
	return out, err
}

// TranscriptReadOutcome is what a finished reading has to report.
type TranscriptReadOutcome struct {
	// Status is done or failed. Done with no proposals is the honest answer
	// for a transcript that states no next steps, and Detail says so.
	Status string
	// Detail explains the outcome in words a rep can act on. Required for
	// failed, and for a done reading that produced nothing — an empty result
	// that does not explain itself reads as a broken feature.
	Detail string
	// ProposalIDs are the approvals staged, in the order they were staged.
	ProposalIDs []ids.UUID
}

// FinishTranscriptRead records what the reading produced and closes it.
func (s *Store) FinishTranscriptRead(ctx context.Context, readID ids.UUID, outcome TranscriptReadOutcome) error {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return err
	}
	if outcome.Status != TranscriptReadDone && outcome.Status != TranscriptReadFailed {
		return fmt.Errorf("activities: a transcript read finishes done or failed, not %q", outcome.Status)
	}
	if outcome.Detail == "" && (outcome.Status == TranscriptReadFailed || len(outcome.ProposalIDs) == 0) {
		return errors.New("activities: a failed or empty transcript read must say why, or its result cannot be told from a broken one")
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		proposals := outcome.ProposalIDs
		if proposals == nil {
			proposals = []ids.UUID{}
		}
		var detail *string
		if outcome.Detail != "" {
			detail = &outcome.Detail
		}
		tag, err := tx.Exec(ctx, `
			UPDATE transcript_read
			   SET status = $2, status_detail = $3, proposal_ids = $4, finished_at = now()
			 WHERE id = $1 AND status = 'running'`,
			readID, outcome.Status, detail, proposals)
		if err != nil {
			return fmt.Errorf("finish transcript read: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: transcript read %s is not running", apperrors.ErrConflict, readID)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "transcript_read", readID, nil, map[string]any{
			"status": outcome.Status, "proposals": len(proposals),
		}); err != nil {
			return fmt.Errorf("audit transcript read finish: %w", err)
		}
		return nil
	})
}

// GetTranscriptRead answers the client's poll. It is a read of a record, so it
// carries the row-scope gate like every other one: a reading of a transcript
// the caller cannot see does not exist.
func (s *Store) GetTranscriptRead(ctx context.Context, activityID ids.ActivityID, readID ids.UUID) (TranscriptRead, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return TranscriptRead{}, err
	}
	var out TranscriptRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readActivity(ctx, tx, activityID, storekit.LiveOnly); err != nil {
			return err
		}
		var err error
		out, err = scanTranscriptRead(tx.QueryRow(ctx, `
			SELECT `+transcriptReadColumns+`
			  FROM transcript_read
			 WHERE id = $1 AND activity_id = $2`, readID, activityID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: transcript read %s", apperrors.ErrNotFound, readID)
		}
		if err != nil {
			return fmt.Errorf("read transcript read: %w", err)
		}
		return nil
	})
	return out, err
}

// LatestTranscriptRead answers "has this transcript been read, and how did it
// go" when the surface loads without a read id in hand. ErrNotFound means
// never read, which the client renders as the offer to read it.
func (s *Store) LatestTranscriptRead(ctx context.Context, activityID ids.ActivityID) (TranscriptRead, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return TranscriptRead{}, err
	}
	var out TranscriptRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readActivity(ctx, tx, activityID, storekit.LiveOnly); err != nil {
			return err
		}
		var err error
		out, err = scanTranscriptRead(tx.QueryRow(ctx, `
			SELECT `+transcriptReadColumns+`
			  FROM transcript_read
			 WHERE activity_id = $1
			 ORDER BY created_at DESC
			 LIMIT 1`, activityID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no transcript read for activity %s", apperrors.ErrNotFound, activityID)
		}
		if err != nil {
			return fmt.Errorf("read latest transcript read: %w", err)
		}
		return nil
	})
	return out, err
}
