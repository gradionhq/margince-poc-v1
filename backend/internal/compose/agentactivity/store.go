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

// The two free-text columns this read forwards are capped on the way to the
// wire. Neither is server-authored prose of bounded length: summary is written
// by a model that may spend a 50k-token output on it, and a record a prompt
// injection reached can inflate it further — and up to recentBound of them ship
// to every open tab on every poll. A reader needs the first paragraph, not the
// transcript, so the panel gets a bounded string and the row keeps everything.
const (
	summaryBound       = 2000
	degradeReasonBound = 500
)

// Item is one occurrence of scheduled agent work, as facts. The reader's locale
// decides the words, so nothing here is a sentence.
type Item struct {
	ID         ids.UUID
	Kind       string
	State      State
	StartedAt  time.Time
	FinishedAt *time.Time
	// DegradeReason is one of the runner's own closed reasons — never a
	// provider's or a parser's message, because those carry vendor text and can
	// echo credential material, and this read hands the column to an ordinary
	// rep. The runner holds that end (runner.degradeFromCause); this end assumes
	// nothing and still caps the length.
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
//
// ONE ANSWER, ONE SNAPSHOT: all three statements run inside a single
// transaction. Across separate transactions a run that is `running` when the
// first statement executes and `completed` when the third does appears in BOTH
// lists, and the panel then says "putting your brief together" and "your brief
// is ready" about one occurrence at once; the mirror interleaving makes an
// occurrence vanish for a poll. That is the same double-report the queued-vs-
// claimed split defends against, and a transaction boundary is no more allowed
// to reopen it than a status boundary is.
func (s *Store) Mine(ctx context.Context, userID ids.UUID) (running, recent []Item, err error) {
	var inFlight, queued []Item
	if err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var txErr error
		if inFlight, txErr = read(ctx, tx, runningSQL, RunState, userID); txErr != nil {
			return fmt.Errorf("read runs in flight: %w", txErr)
		}
		if queued, txErr = read(ctx, tx, queuedSQL, JobState, userID); txErr != nil {
			return fmt.Errorf("read queued jobs: %w", txErr)
		}
		if recent, txErr = read(ctx, tx, recentSQL, RunState, userID, s.startOfToday(), recentBound); txErr != nil {
			return fmt.Errorf("read what settled today: %w", txErr)
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("agentactivity: %w", err)
	}
	running = make([]Item, 0, len(inFlight)+len(queued))
	running = append(running, inFlight...)
	running = append(running, queued...)
	newestFirst(running)
	return running, recent, nil
}

// startOfToday is midnight in the clock's own location, which is what "today"
// means to the person reading the rail.
func (s *Store) startOfToday() time.Time {
	now := s.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// read runs one of the three statements on the caller's open transaction and
// maps its rows through the state vocabulary that statement's table speaks. A
// row whose status the mapper does not know is DROPPED: this surface has no word
// for it, and inventing one is how a "Done" that never happened reaches a
// screen.
func read(ctx context.Context, tx pgx.Tx, query string, mapState func(string) (State, bool), args ...any) ([]Item, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows, mapState)
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
		item.DegradeReason = capped(item.DegradeReason, degradeReasonBound)
		item.Summary = capped(summaryOf(result), summaryBound)
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

// capped bounds a free-text column on its way to the wire, counting RUNES so a
// cut never lands mid-character and hands a client invalid UTF-8. The ellipsis
// is inside the bound: a truncated string says so, because a reader who cannot
// tell would take a sentence that stops mid-word as what the run actually wrote.
func capped(text *string, bound int) *string {
	if text == nil {
		return nil
	}
	runes := []rune(*text)
	if len(runes) <= bound {
		return text
	}
	trimmed := string(runes[:bound-1]) + "\u2026"
	return &trimmed
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
