// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AUTO-AC-6 end to end ("Sam turns on 'no activity for N days', and the
// reminder task lands in his work queue", features/10 §1's own worked
// example) — driven through the REAL River `time_scan` periodic job, not
// TimeScanner.Scan called directly. That direct call is already proven by
// Task 14a's own suite (timescan_integration_test.go, same package): the
// scan/dispatch/occurrence-key machinery in isolation. This suite instead
// boots compose.NewJobRunner exactly as cmd/worker does, so the periodic
// pass that actually reaches Sam's reminder is the one wired into the
// real binary, never a hand-called method standing in for it.
//
// Completion is observed on River's own subscription channel
// (SubscribeCompleted, subscribed before Start so the RunOnStart pass is
// never missed) — no time.Sleep, no polling. NewJobRunner wires
// TimeScanner over the real wall clock (compose/timescan.go's
// NewTimeScanner, unlike NewTimeScannerWithClock's injectable one used by
// the direct-Scan suite), so the seeded quiet touch is placed comfortably
// past the threshold relative to time.Now() rather than a pinned instant.
//
// Sam authors this automation himself (owner_id = Sam), which routes the
// firing through the Task-13 match-time gate (automation/gate.go): the
// gate re-resolves Sam's LIVE RBAC through identity.Service, the real
// resolver compose.NewWorkflowEngine wires in — not the harness's
// synthetic principal.Permissions used elsewhere for read-side scope
// checks. So Sam needs an actual role + role_assignment row, seeded
// below, or the gate blocks his own automation as a lost permission.
//
// "Sam's work queue" is not a bespoke query invented for this test: it is
// the SAME read the built Tasks screen performs today
// (frontend/src/screens/tasks.tsx: GET /activities?kind=task), which
// resolves to activities.Store.ListActivities scoped by
// auth.ActivityScopeClause — an activity is visible to a viewer when ANY
// entity it links to is visible under that viewer's own row-scope. This
// suite asserts against that real scope clause (not a tautological
// "the list is non-empty"): Sam's own read must surface the reminder,
// and an unrelated rep's identical read must NOT.
//
// A known gap this suite deliberately does not paper over: the OpenAPI
// contract also declares an `assignee_id` filter on this endpoint ("Open
// tasks for an assignee", crm.yaml) for the still-`status: planned`
// personal-queue epic (subsystems/tasks-and-work-queue.md), but neither
// crmcontracts.ListActivitiesParams.AssigneeId nor
// activities.ListActivitiesInput carries it through to a WHERE clause —
// and no clock handler stamps assignee_id on the task it creates
// (handlers_clock.go's taskCreateEffect carries no assignee_id
// key at all). Sam's reminder reaches him ONLY because he owns the deal
// it is linked to, never because anyone assigned it to him. See the
// task-17 report for the full writeup; that gap is out of THIS suite's
// scope to close.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestNoActivityReminderReachesTheOwnersTasksScreenThroughTheRealRiverJob(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	sam := e.Rep1 // owns the quiet deal below; shares Team1 with Rep2

	dealID := e.SeedDeal(t, "Sam's Quiet Renewal", pipeline, open, &sam)
	owner := OwnerConn(t)

	// The number Sam "fills in" (features/10 §1's own phrasing) — a
	// genuine human touch placed comfortably past it. NewJobRunner's
	// TimeScanner rides the real wall clock, so the margin must survive
	// whenever this suite actually executes, not just at authoring time.
	const noActivityDays = 5
	staleTouch := time.Now().AddDate(0, 0, -(noActivityDays + 8))
	seedGenuineTouch(t, owner, e.WS, dealID, "call", staleTouch)

	seedTaskCreatePermission(t, owner, e.WS, sam)
	seedOwnedNoActivityReminder(t, owner, e.WS, sam, noActivityDays)

	applyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := compose.NewJobRunner(e.Pool, quiet, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	// Subscribe before Start so the RunOnStart completion is never
	// missed — RunOnStart fires time_scan immediately at boot, and this
	// channel says exactly when it finished.
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()

	ctx := context.Background()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, compose.TimeScanArgs{}.Kind())

	// The firing must have cleared Sam's own gate as 'applied', not
	// 'blocked' — if the seeded role above did not actually grant
	// create_task, this is where that would surface, rather than as a
	// mysterious missing task below.
	var runStatus string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status FROM workflow_run WHERE handler = 'no_activity_reminder'`).Scan(&runStatus)
	}); err != nil {
		t.Fatalf("reading the firing's run status: %v", err)
	}
	if runStatus != "applied" {
		t.Fatalf("no_activity_reminder run status = %q, want %q — Sam's own match-time gate must not block his own automation", runStatus, "applied")
	}

	taskID := reminderTaskID(t, e, dealID)

	samCtx := e.As(sam, []ids.UUID{e.Team1}, repPermsWithActivity())
	samTasks, _, err := e.Activities.ListActivities(samCtx, activities.ListActivitiesInput{Kind: strPtr("task")})
	if err != nil {
		t.Fatalf("Sam listing his own tasks: %v", err)
	}
	if !taskListContains(samTasks, taskID) {
		t.Fatalf("Sam's Tasks screen (GET /activities?kind=task) did not surface reminder task %s — "+
			"AUTO-AC-6 requires Sam to actually GET the reminder, not merely a workflow_run row claiming the engine acted", taskID)
	}

	// The identical read for an unrelated rep (a different team, no
	// stake in Sam's deal) must NOT surface it — proving this is a real
	// row-scoped queue, not an unbounded list any assertion would pass
	// against.
	strangerCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, repPermsWithActivity())
	strangerTasks, _, err := e.Activities.ListActivities(strangerCtx, activities.ListActivitiesInput{Kind: strPtr("task")})
	if err != nil {
		t.Fatalf("an unrelated rep listing tasks: %v", err)
	}
	if taskListContains(strangerTasks, taskID) {
		t.Fatalf("an unrelated rep's Tasks screen surfaced Sam's reminder task %s — the scope clause is not actually row-limited", taskID)
	}
}

// applyRiverSchema layers River's schema onto the harness-migrated
// database, exactly as cmd/migrate does after core+custom. Mirrors
// compose/jobs_integration_test.go's helper of the same name (a
// different package — no collision); duplicated rather than exported
// because this is the only suite in this package driving a real River
// runner, and platform/jobs.Migrate already owns the one real
// implementation both copies call.
func applyRiverSchema(t *testing.T) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	// The compose/integration package shares ONE clone DB across its tests, and
	// more than one drives the real River runner (no_activity_reminder here,
	// gmail_watch). River's migrator recreates river_migration on a re-apply
	// (SQLSTATE 42P07), so ensure-once on the table's existence rather than
	// migrating twice — these tests run sequentially in-package (no t.Parallel).
	var present bool
	if err := ownerPool.QueryRow(ctx, `SELECT to_regclass('public.river_migration') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("checking river schema: %v", err)
	}
	if present {
		return
	}
	if _, err := jobs.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("applying river schema: %v", err)
	}
}

// awaitKindCompleted blocks until a job of the given kind reports
// completion, or the context deadline fires. No polling, no sleep.
func awaitKindCompleted(ctx context.Context, t *testing.T, sub <-chan *river.Event, kind string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q to complete: %v", kind, ctx.Err())
		case ev := <-sub:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				return
			}
		}
	}
}

// seedTaskCreatePermission grants ONE human the create_task permission
// (activity/create) through the REAL identity tables — role +
// role_assignment. The Task-13 match-time gate resolves a human-owned
// automation's owner through identity.Service, reading these tables
// directly, so a human-authored firing needs an actual role row or the
// gate blocks it as a lost permission.
func seedTaskCreatePermission(t *testing.T, owner *pgx.Conn, ws, user ids.UUID) {
	t.Helper()
	var roleID ids.UUID
	if err := owner.QueryRow(context.Background(),
		`INSERT INTO role (workspace_id, key, name, permissions)
		 VALUES ($1, 'reminder_owner', 'Reminder Owner',
		         '{"objects":{"activity":{"create":true,"read":true}},"row_scope":"own"}'::jsonb)
		 RETURNING id`, ws).Scan(&roleID); err != nil {
		t.Fatalf("seeding the create_task test role: %v", err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
		ws, roleID, user); err != nil {
		t.Fatalf("assigning the create_task test role: %v", err)
	}
}

// seedOwnedNoActivityReminder enrolls one enabled no_activity_reminder
// instance OWNED BY A HUMAN — unlike seedNoActivityReminder's ownerless
// system-seed shape (timescan_integration_test.go), a non-null owner_id
// routes every firing through the Task-13 match-time gate, which is
// exactly the path AUTO-AC-6 exercises: Sam turned this rule on himself.
func seedOwnedNoActivityReminder(t *testing.T, owner *pgx.Conn, ws, ownerID ids.UUID, days int) {
	t.Helper()
	params := fmt.Sprintf(`{"no_activity_days":%d}`, days)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, owner_id, enabled)
		 VALUES ($1, $2, 'no_activity_reminder', 'No Activity Reminder', '{"schedule":"clock"}', '{"kind":"create_task"}', $3::jsonb, $4, true)`,
		ids.NewV7(), ws, params, ownerID); err != nil {
		t.Fatalf("seeding the owner-authored no_activity_reminder instance: %v", err)
	}
}

// reminderTaskID resolves the single create_task reminder
// no_activity_reminder minted on the deal's timeline — the same join
// reminderTaskCount (timescan_integration_test.go) counts, but returning
// the row's id so the work-queue assertions above can look for THIS
// specific task rather than any task that happens to exist.
func reminderTaskID(t *testing.T, e *Env, dealID ids.UUID) ids.UUID {
	t.Helper()
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("reminder tasks on the deal = %d, want exactly 1 before resolving its id", got)
	}
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT a.id FROM activity a
			JOIN activity_link al ON al.activity_id = a.id
			WHERE al.entity_type = 'deal' AND al.deal_id = $1 AND a.kind = 'task'`, dealID).Scan(&id)
	}); err != nil {
		t.Fatalf("resolving the reminder task's id: %v", err)
	}
	return id
}

// taskListContains reports whether id is among the activities a
// work-queue read returned — the assertions above check for ONE
// specific task rather than merely "the list is non-empty".
func taskListContains(tasks []crmcontracts.Activity, id ids.UUID) bool {
	for _, a := range tasks {
		if ids.UUID(a.Id) == id {
			return true
		}
	}
	return false
}
