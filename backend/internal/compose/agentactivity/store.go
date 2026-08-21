// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// recentBound caps what settled today. An unbounded per-person run history is
// the per-person activity ledger this installation deliberately does not keep,
// so this is a requirement and not a page size.
const recentBound = 10

// Item is one occurrence of scheduled agent work, as facts. The reader's locale
// decides the words, so nothing here is a sentence.
type Item struct {
	ID         ids.UUID
	Kind       string
	State      State
	StartedAt  time.Time
	FinishedAt *time.Time
	// DegradeReason is server-authored operator vocabulary, so it belongs in the
	// panel's runtime detail and never inside a reader-facing line.
	DegradeReason *string
	// Summary is the run's own prose when it wrote any. It is optional by
	// construction: nothing validates that a finishing run produced one.
	Summary *string
}

// Store reads one person's agent activity. It writes nothing, so it has no
// audit or outbox ride-along; it owns no table either.
type Store struct {
	db *database.DB
	// now bounds "today". Injected so a test states the instant it means rather
	// than racing the wall clock across midnight.
	now func() time.Time
}

// NewStore opens the read on an already-bound database handle.
func NewStore(db *database.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

// The filter is passport.on_behalf_of — "the human whose RBAC bounds this
// passport" (core 0003). There is no passport.user_id. granted_by is a DIFFERENT
// human (whoever minted the passport), and filtering on it would show a manager
// their team's runs as though they were their own.
//
// The join is an inner join on purpose. agent_run.passport_id is nullable with
// ON DELETE SET NULL, so a deleted passport orphans its runs; such a run is
// genuinely unattributable and reading NULL as "mine" would hand one person
// another person's work.
//
// idx_passport_obo already indexes (on_behalf_of) WHERE revoked_at IS NULL. The
// join deliberately does NOT filter on revoked_at: a revoked passport's finished
// run is still that person's history, and hiding it would make the feed lie
// about what ran today.
//
// Neither agent_run nor runner_job carries workspace_id any more (core 0246), so
// there is no tenant predicate to bind here.
const runningSQL = `
  SELECT r.id, r.agent_spec, r.status, r.created_at, r.finished_at,
         r.degrade_reason, r.result
    FROM agent_run r
    JOIN passport p ON p.id = r.passport_id
   WHERE p.on_behalf_of = $1
     AND r.status IN ('running', 'awaiting_approval')
   ORDER BY r.created_at DESC`

// queuedSQL reads the trigger queue, which is where "queued" lives — agent_run
// has no such status because the row is born already running. due_at stands in
// for started_at: it is when this occurrence became runnable, the only time the
// job knows. The three trailing NULLs keep one row shape for one scanner; a job
// has no outcome yet, so it has none of them.
//
// Only 'queued' is read. A claimed job has an agent_run row and that row is the
// authority — reporting both would show one trigger occurrence twice.
const queuedSQL = `
  SELECT j.id, j.agent_spec, j.status, j.due_at, NULL::timestamptz,
         NULL::text, NULL::jsonb
    FROM runner_job j
    JOIN passport p ON p.id = j.passport_id
   WHERE p.on_behalf_of = $1
     AND j.status = 'queued'
   ORDER BY j.due_at DESC`

// recentSQL reads what settled since local midnight, newest first.
const recentSQL = `
  SELECT r.id, r.agent_spec, r.status, r.created_at, r.finished_at,
         r.degrade_reason, r.result
    FROM agent_run r
    JOIN passport p ON p.id = r.passport_id
   WHERE p.on_behalf_of = $1
     AND r.status IN ('completed', 'degraded', 'failed')
     AND r.created_at >= $2
   ORDER BY r.created_at DESC
   LIMIT $3`

// Mine is what the scheduled agent is doing for this person now, and what it
// finished for them today. The three reads are unioned in Go rather than in SQL
// so each keeps its own state vocabulary and its own mapper.
func (s *Store) Mine(ctx context.Context, userID ids.UUID) (running, recent []Item, err error) {
	inFlight, err := s.read(ctx, runningSQL, RunState, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("agentactivity: read runs in flight: %w", err)
	}
	queued, err := s.read(ctx, queuedSQL, JobState, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("agentactivity: read queued jobs: %w", err)
	}
	recent, err = s.read(ctx, recentSQL, RunState, userID, s.startOfToday(), recentBound)
	if err != nil {
		return nil, nil, fmt.Errorf("agentactivity: read what settled today: %w", err)
	}
	running = append(inFlight, queued...)
	newestFirst(running)
	return running, recent, nil
}

// startOfToday is midnight in the clock's own location, which is what "today"
// means to the person reading the rail.
func (s *Store) startOfToday() time.Time {
	now := s.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// read runs one of the three statements and maps its rows through the state
// vocabulary that statement's table speaks. A row whose status the mapper does
// not know is DROPPED: this surface has no word for it, and inventing one is how
// a "Done" that never happened reaches a screen.
func (s *Store) read(ctx context.Context, query string, mapState func(string) (State, bool), args ...any) ([]Item, error) {
	var items []Item
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items, err = collect(rows, mapState)
		return err
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func collect(rows pgx.Rows, mapState func(string) (State, bool)) ([]Item, error) {
	var items []Item
	for rows.Next() {
		var (
			item   Item
			status string
			result []byte
		)
		if err := rows.Scan(&item.ID, &item.Kind, &status, &item.StartedAt,
			&item.FinishedAt, &item.DegradeReason, &result); err != nil {
			return nil, err
		}
		state, known := mapState(status)
		if !known {
			continue
		}
		item.State = state
		item.Summary = summaryOf(result)
		items = append(items, item)
	}
	return items, rows.Err()
}

// summaryOf pulls the run's prose out of the result column. SaveOutcome stores
// the model's `final` object verbatim, so the summary sits at its top level.
//
// Any shape this does not recognise yields no summary rather than an error: a
// finishing run is never required to write one (parseStep validates only that
// `final` is present), and a degrade writes a partial-state object that has none
// at all. A malformed result is a run that produced no summary, not a broken
// read of everything else on the row.
func summaryOf(result []byte) *string {
	if len(result) == 0 {
		return nil
	}
	var final struct {
		Summary *string `json:"summary"`
	}
	if err := json.Unmarshal(result, &final); err != nil {
		return nil
	}
	return final.Summary
}

// newestFirst orders the merged running feed. Each statement already sorts, but
// the union of two of them does not, and the id breaks the tie so two items
// stamped the same instant do not swap places between polls.
func newestFirst(items []Item) {
	sort.Slice(items, func(a, b int) bool {
		if items[a].StartedAt.Equal(items[b].StartedAt) {
			return items[a].ID.String() > items[b].ID.String()
		}
		return items[a].StartedAt.After(items[b].StartedAt)
	})
}
