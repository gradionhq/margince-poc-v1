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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// recentBound caps what settled today. An unbounded per-person history is the
// per-person activity ledger this installation deliberately does not keep, so
// this is a requirement rather than a page size.
const recentBound = 10

// liveBound caps what is reported as in flight.
//
// The live set is not bounded by anything a person controls: one rep can press
// "read this document" on twenty attachments, and every live row ships to every
// open tab on every poll. Higher than recentBound because a live occurrence is
// the thing the reader is actually waiting on, and cutting one is worse than
// cutting a finished one.
const liveBound = 25

// The two free-text columns this read forwards are capped on the way to the
// wire. Neither is server-authored prose of bounded length: summary can be a
// model's whole output, and an occurrence a prompt injection reached can
// inflate it further — and up to recentBound of them ship to every open tab on
// every poll. A reader needs the first paragraph, not the transcript, so the
// wire gets a bounded string and the row keeps everything.
const (
	// Exported so a root-package fitness test can hold them to the maxLength the
	// contract publishes — the read and the contract are two statements of one
	// cap, and only the root can see both.
	SummaryBound       = 2000
	DegradeReasonBound = 500
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

// feedSQL reads both halves of the feed in ONE statement, and that is what
// makes "one occurrence, one line" true rather than asserted.
//
// The transaction is READ COMMITTED — platform/database opens it with a bare
// pool.Begin and nothing sets an isolation level — so two statements would take
// two snapshots, and an occurrence that settled between them would appear in
// both: the rail would say "reading your document" and "I've read your
// document" about one reading at once. One statement is one snapshot, so the
// window does not exist to be closed.
//
// Two arms rather than one predicate, because each needs its own ordering and
// its own bound, and because each matches one of the table's partial indexes:
//
//	live     queued IS live — an occurrence waiting for a worker is work in
//	         progress to the person who asked — and ai_task_run_live indexes
//	         exactly this predicate. `stalled` is decided here, in SQL, against
//	         the DATABASE clock: stale_after was computed from timestamps the
//	         database stamped, and comparing them to a reader's host clock
//	         would answer a different question on every machine.
//
//	settled  bounded by finished_at, because "what the AI finished for me
//	         today" is a question about when it finished. An occurrence that
//	         started at 23:50 and finished at 00:10 belongs in today's feed;
//	         keyed on its start it would fall out of settled AND have already
//	         left live, so it would vanish from the rail entirely.
const feedSQL = `
(
  SELECT true AS live, id, kind,
         CASE WHEN stale_after IS NOT NULL AND stale_after < now() THEN 'stalled' ELSE state END,
         COALESCE(started_at, queued_at), finished_at,
         left(degrade_reason, $4), left(summary, $5)
    FROM ai_task_run
   WHERE actor_user_id = $1
     AND state IN ('queued','running')
   ORDER BY queued_at DESC, id DESC
   LIMIT $6
)
UNION ALL
(
  SELECT false AS live, id, kind, state,
         COALESCE(started_at, queued_at), finished_at,
         left(degrade_reason, $4), left(summary, $5)
    FROM ai_task_run
   WHERE actor_user_id = $1
     AND state IN ('done','degraded','failed')
     AND finished_at >= $2
   ORDER BY finished_at DESC, id DESC
   LIMIT $3
)`

// Mine is what the AI is doing for THE CALLER now, and what it finished for
// them today.
//
// The person is taken from the bound principal and is NOT a parameter, which is
// the whole of the authorization. A store method that accepted a user id would
// let any in-process caller ask for somebody else's feed, and the only thing
// standing between that and a leak would be every caller remembering to pass
// its own — the shape this repo gates against everywhere else. Here there is
// nothing to remember: another person's feed cannot be expressed.
func (s *Store) Mine(ctx context.Context, startOfToday time.Time) (live, settled []Item, err error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return nil, nil, fmt.Errorf("aiactivity: a personal read needs an authenticated person")
	}
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, txErr := tx.Query(ctx, feedSQL,
			actor.UserID, startOfToday, recentBound, DegradeReasonBound, SummaryBound, liveBound)
		if txErr != nil {
			return txErr
		}
		defer rows.Close()
		live, settled = []Item{}, []Item{}
		for rows.Next() {
			var item Item
			var isLive bool
			if scanErr := rows.Scan(&isLive, &item.ID, &item.Kind, &item.State,
				&item.StartedAt, &item.FinishedAt, &item.DegradeReason, &item.Summary); scanErr != nil {
				return scanErr
			}
			if isLive {
				live = append(live, item)
				continue
			}
			settled = append(settled, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("aiactivity: %w", err)
	}
	return live, settled, nil
}
