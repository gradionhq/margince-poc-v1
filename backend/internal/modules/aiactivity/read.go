// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// One person's view of what the AI is doing for them.
//
// It reads ONE table. The vocabulary of "what kinds of AI work exist" lives at
// the emitters, not here — a new kind adds a publisher and this read does not
// change, which is the whole reason the projection exists rather than a union
// over every source's own tables.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// recentBound caps what settled today. An unbounded per-person history is the
// per-person activity ledger this installation deliberately does not keep, so
// this is a requirement rather than a page size.
const recentBound = 10

// The two free-text columns this read forwards are capped on the way to the
// wire. Neither is server-authored prose of bounded length: summary can be a
// model's whole output, and an occurrence a prompt injection reached can
// inflate it further — and up to recentBound of them ship to every open tab on
// every poll. A reader needs the first paragraph, not the transcript, so the
// wire gets a bounded string and the row keeps everything.
const (
	summaryBound       = 2000
	degradeReasonBound = 500
)

// Item is one occurrence, as facts. The reader's locale decides the words, so
// nothing here is a sentence.
type Item struct {
	ID            ids.UUID
	Kind          string
	State         string
	StartedAt     time.Time
	FinishedAt    *time.Time
	DegradeReason *string
	Summary       *string
}

// StateStalled is derived at READ time and never stored.
//
// Nothing writes it, so nothing can forget to: a live occurrence past the lease
// its own source declared is reported stalled, unconditionally, without a
// second query and even if every other recovery mechanism has failed. That is
// what stops a worker which died mid-run from being displayed as working.
const StateStalled = "stalled"

// liveSQL reads what is still expected to move for one person.
//
// queued IS live — an occurrence waiting for a worker is work in progress to
// the person who asked — and ai_task_run_live indexes exactly this predicate.
//
// The stalled decision is made in SQL against the DATABASE clock, not in Go
// against the caller's: stale_after was computed from timestamps the database
// stamped, and comparing them to a reader's host clock would answer a different
// question on every machine.
const liveSQL = `
  SELECT id, kind,
         CASE WHEN stale_after IS NOT NULL AND stale_after < now() THEN 'stalled' ELSE state END,
         COALESCE(started_at, queued_at), finished_at,
         left(degrade_reason, $2), left(summary, $3)
    FROM ai_task_run
   WHERE actor_user_id = $1
     AND state IN ('queued','running')
   ORDER BY queued_at DESC, id DESC`

// settledSQL reads what finished for this person today, newest first.
//
// The bound is finished_at because "what the AI finished for me today" is a
// question about when it finished. An occurrence that started at 23:50 and
// finished at 00:10 belongs in today's feed; keyed on its start it would fall
// out of settled AND have already left live, so the occurrence would vanish
// from the rail entirely.
const settledSQL = `
  SELECT id, kind, state, COALESCE(started_at, queued_at), finished_at,
         left(degrade_reason, $4), left(summary, $5)
    FROM ai_task_run
   WHERE actor_user_id = $1
     AND state IN ('done','degraded','failed')
     AND finished_at >= $2
   ORDER BY finished_at DESC, id DESC
   LIMIT $3`

// Mine is what the AI is doing for this person now, and what it finished for
// them today.
//
// The two reads share one transaction, and the LIVE one runs first. That order
// closes the mirror of the double-report problem: the transaction is READ
// COMMITTED, so each statement takes its own snapshot, and an occurrence that
// settles between them is caught by the later read rather than falling through
// the gap. It cannot be reported twice either — the states the two predicates
// select are disjoint, and one occurrence is one row.
func (s *Store) Mine(ctx context.Context, userID ids.UUID, startOfToday time.Time) (live, settled []Item, err error) {
	if userID.IsZero() {
		return nil, nil, fmt.Errorf("aiactivity: a personal read needs a person")
	}
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		var txErr error
		if live, txErr = collect(ctx, tx, liveSQL, userID, degradeReasonBound, summaryBound); txErr != nil {
			return fmt.Errorf("read what is live: %w", txErr)
		}
		if settled, txErr = collect(ctx, tx, settledSQL,
			userID, startOfToday, recentBound, degradeReasonBound, summaryBound); txErr != nil {
			return fmt.Errorf("read what settled today: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("aiactivity: %w", err)
	}
	return live, settled, nil
}

// collect runs one of the two statements and scans its rows.
func collect(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]Item, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Kind, &item.State,
			&item.StartedAt, &item.FinishedAt, &item.DegradeReason, &item.Summary); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
