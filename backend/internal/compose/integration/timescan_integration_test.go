// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The end-to-end proof for Task 14a's clock-trigger machinery: a real
// TimeScanner.Scan pass (compose.NewTimeScanner, over the real
// ActivityScan seam sourced from activities' own tables) finds a deal
// whose only linked activity is stale, fires no_activity_reminder through
// the SAME runOne path event triggers use, and lands a real create_task
// reminder. A second pass over the SAME anchor must NOT refire — the
// occurrence key (Task 12) holding across the real scan, not just the
// scripted proof in automation/occurrence_integration_test.go.

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

func TestTimeScannerFiresNoActivityReminderOnceThenHoldsTheOccurrenceKey(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Gone Quiet Deal", pipeline, open, nil)

	owner := OwnerConn(t)
	// The deal's only linked activity is stale — 10 days old, older than
	// no_activity_reminder's 7-day default (no params seeded below), so
	// this deal is exactly the candidate LastTouchBefore must surface.
	staleSince := time.Now().UTC().AddDate(0, 0, -10)
	touchID := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, 'call', 'Last real touch', $3, 'manual', 'human:x')`,
		touchID, e.WS, staleSince); err != nil {
		t.Fatalf("seeding the stale activity: %v", err)
	}
	LinkActivity(t, owner, e.WS, touchID, "deal", dealID)

	// A system-seeded instance (no owner_id): the match-time owner gate
	// (automation/gate.go) skips entirely for a zero OwnerID, so this test
	// proves the scan/dispatch machinery without also standing up a real
	// RBAC fixture for an owning human — that gate is proven elsewhere
	// (automation/gate_integration_test.go).
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, enabled)
		 VALUES ($1, $2, 'no_activity_reminder', 'No Activity Reminder', '{"schedule":"clock"}', '{"kind":"create_task"}', '{}'::jsonb, true)`,
		ids.NewV7(), e.WS); err != nil {
		t.Fatalf("seeding the no_activity_reminder instance: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScanner(e.Pool, quiet)

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("reminder tasks linked to the deal after the first scan = %d, want exactly 1 — the reminder must land", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 1 {
		t.Fatalf("workflow_run rows for no_activity_reminder after the first scan = %d, want exactly 1", got)
	}

	// Second pass: the SAME anchor (the activity never moved) must not
	// refire — the occurrence key (Task 12) holds across a real scan.
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("reminder tasks linked to the deal after the SECOND scan = %d, want still exactly 1 — the unchanged anchor must not refire", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 1 {
		t.Fatalf("workflow_run rows for no_activity_reminder after the second scan = %d, want still exactly 1", got)
	}
}

// reminderTaskCount counts the create_task activities no_activity_reminder
// would have minted on the deal's own timeline — kind='task' distinguishes
// the reminder from the stale 'call' activity that made the deal a
// candidate in the first place.
func reminderTaskCount(t *testing.T, e *Env, dealID ids.UUID) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE al.entity_type = 'deal' AND al.deal_id = $1 AND a.kind = 'task'`, dealID)
}

// runCountForHandler reads workflow_run rows for one handler name — the
// durable claim runOne makes per (handler, idempotency_key), read here
// through a workspace transaction since RLS forces it even for the
// owner-less pool path.
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
