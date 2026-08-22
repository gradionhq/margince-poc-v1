// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Actor scopes. 'personal' with a zero user means the person is GONE and the
// occurrence is shown to nobody; 'workspace' means it belonged to nobody from
// the start. Two facts, never one nullable column, because conflating them
// relabels a leaver's work as a system sweep.
const (
	ScopePersonal  = "personal"
	ScopeWorkspace = "workspace"
)

// Change is one state change, already resolved: the actor comes from the
// envelope and the rest from the payload, so the store never re-derives either.
type Change struct {
	Source        string
	OccurrenceKey string
	Kind          string
	// AITask is the api/ai-tasks.yaml task that did the model work, empty when
	// no model call of the occurrence's own did any.
	AITask      string
	Attempt     int
	ActorScope  string
	ActorUserID ids.UUID
	PassportID  ids.UUID

	State      string
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	StaleAfter *time.Time

	SubjectType  string
	SubjectID    ids.UUID
	Quantity     *int
	QuantityUnit string

	DegradeReason string
	Summary       string

	EventID ids.UUID
}

// Store projects state changes into ai_task_run. It writes no audit or outbox
// row of its own: this is derived read-model state, and the events that feed it
// carry the write shape at their own writers.
type Store struct {
	db *database.DB
}

// NewStore opens the projection on an already-bound database handle.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// applyStateChangeSQL projects one event.
//
// The guard is the tuple comparison, and it is lexicographic on purpose:
// attempt first, then rank within the attempt. A higher attempt always wins,
// even when its state ranks LOWER — that is a release or a re-arm reopening the
// occurrence, and refusing it would leave the row claiming to be running when
// the source says queued. Within one attempt, rank is monotonic and settled is
// terminal. An equal (attempt, rank) redelivery matches no row and updates
// nothing, which is what makes the at-least-once bus harmless here.
//
// Every column is written from EXCLUDED rather than merged: a reopened
// occurrence must LOSE the previous attempt's finished_at, degrade_reason and
// summary, and a partial update is how a row ends up reading as live and failed
// at once.
const applyStateChangeSQL = `
INSERT INTO ai_task_run (
  source, occurrence_key, kind, ai_task, attempt,
  actor_scope, actor_user_id, passport_id,
  state, queued_at, started_at, finished_at, stale_after,
  subject_type, subject_id, quantity, quantity_unit,
  degrade_reason, summary, last_event_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT (source, occurrence_key) DO UPDATE SET
  kind = EXCLUDED.kind, ai_task = EXCLUDED.ai_task, attempt = EXCLUDED.attempt,
  actor_scope = EXCLUDED.actor_scope, actor_user_id = EXCLUDED.actor_user_id,
  passport_id = EXCLUDED.passport_id,
  state = EXCLUDED.state, queued_at = EXCLUDED.queued_at,
  started_at = EXCLUDED.started_at, finished_at = EXCLUDED.finished_at,
  stale_after = EXCLUDED.stale_after,
  subject_type = EXCLUDED.subject_type, subject_id = EXCLUDED.subject_id,
  quantity = EXCLUDED.quantity, quantity_unit = EXCLUDED.quantity_unit,
  degrade_reason = EXCLUDED.degrade_reason, summary = EXCLUDED.summary,
  last_event_id = EXCLUDED.last_event_id,
  seq = nextval('ai_task_run_seq'),
  updated_at = now()
WHERE (EXCLUDED.attempt, ai_task_run_state_rank(EXCLUDED.state))
    > (ai_task_run.attempt, ai_task_run_state_rank(ai_task_run.state))
RETURNING seq`

// ApplyStateChange projects one change and reports whether it landed.
//
// applied=false is the guard working, not a failure: the event was delivered,
// it described a state this occurrence has already moved past, and ACKing it is
// right. Only a real database error is an error.
func (s *Store) ApplyStateChange(ctx context.Context, c Change) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	applied := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var seq int64
		switch err := tx.QueryRow(ctx, applyStateChangeSQL, c.args()...).Scan(&seq); {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("aiactivity: projecting %s/%s: %w", c.Source, c.OccurrenceKey, err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// validate refuses the two shapes the table's CHECKs would refuse anyway, so
// the refusal names the field rather than arriving as a constraint violation a
// consumer can only retry into forever.
func (c Change) validate() error {
	if c.Attempt < 1 {
		return fmt.Errorf("aiactivity: attempt must be at least 1, got %d", c.Attempt)
	}
	if c.ActorScope != ScopePersonal && c.ActorScope != ScopeWorkspace {
		return fmt.Errorf("aiactivity: actor scope %q is neither %q nor %q", c.ActorScope, ScopePersonal, ScopeWorkspace)
	}
	return nil
}

// args orders the change's columns for applyStateChangeSQL. It exists so the
// projection above stays one readable statement rather than a wall of
// arguments — and so the two cannot drift, because there is only one caller.
func (c Change) args() []any {
	return []any{
		c.Source, c.OccurrenceKey, c.Kind, textOrNil(c.AITask), c.Attempt,
		c.ActorScope, uuidOrNil(c.ActorUserID), uuidOrNil(c.PassportID),
		c.State, c.QueuedAt, c.StartedAt, c.FinishedAt, c.StaleAfter,
		textOrNil(c.SubjectType), uuidOrNil(c.SubjectID), c.Quantity, textOrNil(c.QuantityUnit),
		textOrNil(c.DegradeReason), textOrNil(c.Summary), c.EventID,
	}
}

// abandonedReason is why a live occurrence was closed by the sweep rather than
// by its own source. Server-authored and closed, like every other value that
// reaches this column.
const abandonedReason = "abandoned"

// closeAbandonedSQL settles the live occurrences whose source will never settle
// them.
//
// Only ai_router's, and only past their own lease. A carrier's live row is left
// alone because a carrier holds a durable claim it can re-arm from — that is
// what purgeSettledSQL's comment means by "an occurrence the source still holds
// a claim on". The ROUTER holds no such claim: its start is committed in its own
// transaction before the call, and the only thing that closes it is a flush that
// is best-effort by design. A flush that times out, or a process that dies
// mid-call, therefore leaves a row that renders stalled once its lease expires
// and is reached by nothing afterwards — the live feed has no time bound and the
// settled purge only sees settled rows. Every interrupted call would leave one
// on a rep's rail permanently.
//
// It settles rather than deletes, because the work really did start and saying
// so is more honest than erasing it — and settling hands the row to the ordinary
// retention window instead of inventing a second lifetime for it.
//
// finished_at is set because ai_task_run_settled_has_finish requires it, and the
// lease is cleared because a settled row has nothing left to miss.
const closeAbandonedSQL = `
UPDATE ai_task_run
   SET state = 'failed',
       finished_at = now(),
       stale_after = NULL,
       degrade_reason = $2,
       seq = nextval('ai_task_run_seq'),
       updated_at = now()
 WHERE source = 'ai_router'
   AND state IN ('queued','running')
   AND stale_after IS NOT NULL
   AND stale_after < $1`

// CloseAbandonedRouterRuns settles router occurrences whose lease ran out
// before, returning how many went.
//
// The cutoff is the caller's, not now(): a sweep that closed a row the instant
// its lease expired would race the flush that was about to settle it properly,
// and the caller is the one that knows how much slack the flush is owed.
func (s *Store) CloseAbandonedRouterRuns(ctx context.Context, cutoff time.Time) (int64, error) {
	var closed int64
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, closeAbandonedSQL, cutoff, abandonedReason)
		if err != nil {
			return fmt.Errorf("aiactivity: closing abandoned router occurrences leased before %s: %w", cutoff, err)
		}
		closed = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return closed, nil
}

// purgeSettledSQL drops what the feed no longer reaches. Only settled rows: a
// live one has no finished_at to compare, and deleting one would erase an
// occurrence the source still holds a claim on.
const purgeSettledSQL = `
DELETE FROM ai_task_run
 WHERE state IN ('done','degraded','failed')
   AND finished_at < $1`

// PurgeSettledBefore removes settled occurrences older than cutoff, returning
// how many went.
func (s *Store) PurgeSettledBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var deleted int64
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, purgeSettledSQL, cutoff)
		if err != nil {
			return fmt.Errorf("aiactivity: purging settled occurrences before %s: %w", cutoff, err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// textOrNil maps an empty string to SQL NULL: an absent kind, task or reason is
// absent, and an empty string in the column would satisfy every `IS NOT NULL`
// the read later writes.
func textOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uuidOrNil maps a zero UUID to SQL NULL — a foreign key has no zero value that
// means anything, and the actor columns are nullable exactly so absence is
// expressible.
func uuidOrNil(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}
