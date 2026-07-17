// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The end-to-end proof for Task 14a's clock-trigger machinery and its
// occurrence key (Task 12), over a real migrated Postgres. A deal whose
// last GENUINE engagement (a human 'call') is stale fires
// no_activity_reminder through the SAME runOne path event triggers use,
// landing a real create_task reminder. The load-bearing property this
// suite pins — the one the earlier draft did NOT — is that the reminder's
// OWN task (source "system") does not count as engagement: the deal stays
// a candidate on the second pass, so runOne is invoked AGAIN, and the
// only thing suppressing a duplicate is the anchor-derived idempotency
// key. A key built from ev.ID (a fresh uuid per pass) would add a second
// reminder here; the anchor-derived key does not. A genuinely new human
// touch then moves the anchor and re-arms exactly one more firing.
//
// The clock is pinned (NewTimeScannerWithClock) so "no activity for N
// days" is evaluated against the seeded timestamps, never the wall clock;
// no sleep, no real-time flakiness.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTimeScannerFiresOnceThenTheOccurrenceKeySuppressesTheRefire(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Gone Quiet Deal", pipeline, open, nil)

	owner := OwnerConn(t)

	// The scan evaluates against this pinned instant. The default
	// threshold is 7 days (no params seeded below), so anything older than
	// scanNow-7d is stale.
	scanNow := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return scanNow }

	// One GENUINE human engagement, 10 days before the pinned now — older
	// than the 7-day threshold, so the deal is a candidate. Source
	// 'manual' (a human logging a call) is exactly what MUST reset the
	// clock; it is not the automation engine's own "system" output.
	firstTouch := scanNow.AddDate(0, 0, -10)
	seedGenuineTouch(t, owner, e.WS, dealID, "call", firstTouch)

	// A system-seeded instance (no owner_id): the match-time owner gate
	// (automation/gate.go) skips entirely for a zero OwnerID, so this test
	// exercises the scan/dispatch/occurrence-key machinery without also
	// standing up a real RBAC fixture — that gate is proven separately
	// (automation/gate_integration_test.go).
	seedNoActivityReminder(t, owner, e.WS)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScannerWithClock(e.Pool, now, quiet)

	// Pass 1: the deal is stale, so the reminder fires exactly once.
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("reminder tasks after pass 1 = %d, want exactly 1 — the reminder must land", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 1 {
		t.Fatalf("workflow_run rows after pass 1 = %d, want exactly 1", got)
	}

	// Pass 2 with NOTHING changed. Because LastTouchBefore excludes the
	// engine's own "system" task, the deal's genuine anchor is STILL the
	// -10d call — the deal is STILL a candidate, so runOne IS invoked
	// again. The anchor has not moved, so the anchor-derived idempotency
	// key is identical and claimRun's ON CONFLICT absorbs the second pass.
	// (Were the key ev.ID-based, this pass would mint a fresh key and add
	// a SECOND reminder task + run — which is exactly why the candidate
	// must still be present here for the assertion to have teeth.)
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("reminder tasks after pass 2 = %d, want still exactly 1 — the unchanged anchor must not refire (occurrence key)", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 1 {
		t.Fatalf("workflow_run rows after pass 2 = %d, want still exactly 1", got)
	}

	// A genuinely new human touch — still stale (8 days before now, past
	// the 7-day threshold) but MORE RECENT than the first — moves the
	// anchor. A moved anchor is a new occurrence key, so the trigger
	// re-arms and fires exactly one more time.
	secondTouch := scanNow.AddDate(0, 0, -8)
	seedGenuineTouch(t, owner, e.WS, dealID, "call", secondTouch)

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("third scan (after a new genuine touch): %v", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 2 {
		t.Fatalf("reminder tasks after the anchor moved = %d, want exactly 2 — a new genuine engagement must re-arm the trigger", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 2 {
		t.Fatalf("workflow_run rows after the anchor moved = %d, want exactly 2", got)
	}
}

// seedGenuineTouch inserts one human-logged activity (source 'manual', the
// non-"system" provenance a real touch carries) at the given instant and
// links it to the deal — the engagement LastTouchBefore's anchor is
// computed from.
func seedGenuineTouch(t *testing.T, owner *pgx.Conn, ws, dealID ids.UUID, kind string, at time.Time) {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, $3, 'Genuine engagement', $4, 'manual', 'human:x')`,
		id, ws, kind, at); err != nil {
		t.Fatalf("seeding a genuine %s touch: %v", kind, err)
	}
	LinkActivity(t, owner, ws, id, "deal", dealID)
}

// seedNoActivityReminder enrolls one enabled, ownerless no_activity_reminder
// instance — the configured-and-on state the scan fires off.
func seedNoActivityReminder(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, enabled)
		 VALUES ($1, $2, 'no_activity_reminder', 'No Activity Reminder', '{"schedule":"clock"}', '{"kind":"create_task"}', '{}'::jsonb, true)`,
		ids.NewV7(), ws); err != nil {
		t.Fatalf("seeding the no_activity_reminder instance: %v", err)
	}
}

// reminderTaskCount counts the create_task activities no_activity_reminder
// minted on the deal's timeline — kind='task' distinguishes the reminder
// from the 'call' engagements that make the deal a candidate.
func reminderTaskCount(t *testing.T, e *Env, dealID ids.UUID) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE al.entity_type = 'deal' AND al.deal_id = $1 AND a.kind = 'task'`, dealID)
}

// runCountForHandler reads workflow_run rows for one handler name — the
// durable claim runOne makes per (handler, idempotency_key), read through
// a workspace transaction since RLS forces it even for the owner-less pool
// path.
func runCountForHandler(t *testing.T, e *Env, handler string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_run WHERE handler = $1`, handler).Scan(&n)
	}); err != nil {
		t.Fatalf("counting workflow_run rows for %s: %v", handler, err)
	}
	return n
}
