// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The GDPR retention pass is one job row per tenant. A workspace whose pass
// fails must say so — a retention pass that failed and reported success leaves
// subject data stored past its policy with nothing recording that it happened,
// which is the one failure mode this engine exists to prevent.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// retentionPassCtx is the scope the retention workspace worker binds before it
// calls the engine: the tenant, the system actor, and a fresh correlation id.
// The engine writes an audit row and an outbox event per record it retires, so
// a suite that bound only the workspace would be exercising a pass whose
// provenance production never has.
func retentionPassCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// seedRetentionWorkspace mints an additional tenant. archived names a
// workspace nobody looks at any more, which still stores everything it stored
// the day it was archived.
func seedRetentionWorkspace(t *testing.T, owner *pgx.Conn, name string, archived bool) ids.UUID {
	t.Helper()
	ws := ids.NewV7()
	archivedAt := "NULL"
	if archived {
		archivedAt = "now()"
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO workspace (id, name, slug, base_currency, archived_at)
		VALUES ($1, $2, $3, 'EUR', `+archivedAt+`)`, ws, name, name+"-"+ws.String()); err != nil {
		t.Fatalf("seeding the %s workspace: %v", name, err)
	}
	return ws
}

// seedRetentionTenant plants the lead/unconverted anonymize policy and one
// over-age unconverted lead in ws, through the owner connection so a workspace
// other than the harness's own can be given a due record.
func seedRetentionTenant(t *testing.T, owner *pgx.Conn, ws ids.UUID) ids.UUID {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
		VALUES ($1, 'lead', 'unconverted', 365, 'anonymize')`, ws); err != nil {
		t.Fatalf("seeding the retention policy for %s: %v", ws, err)
	}
	return SeedRow(t, owner, `
		INSERT INTO lead (id, workspace_id, full_name, status, source, captured_by, created_at)
		VALUES ($1, $2, 'Over-age Lead', 'new', 'manual', 'human:x', now() - interval '400 days')`, ws)
}

// failLeadWritesFor makes every lead write in ONE workspace raise, leaving
// every other tenant's writes untouched.
//
// A tenant-selective trigger is the fault seam because nothing in the fixture
// data can produce this failure: the retention pass fails on SQL errors, not on
// record shapes, so a test that only varied the seed could never reach the
// path where one tenant's pass fails and the others succeed. It is dropped in
// cleanup — the integration lane resets rows between tests but keeps the
// schema, so a surviving trigger would fail every later suite that writes a
// lead.
func failLeadWritesFor(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		CREATE OR REPLACE FUNCTION retention_fault_injection() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  RAISE EXCEPTION 'retention fault injection';
		END $$`); err != nil {
		t.Fatalf("creating the fault-injection function: %v", err)
	}
	// Registered before the trigger is armed, not after both: a failure to
	// arm would otherwise leave the function behind, which is the leak this
	// helper's whole cleanup exists to prevent. Cleanups run LIFO, so the
	// trigger below still drops first.
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP FUNCTION retention_fault_injection()`); err != nil {
			t.Errorf("dropping the fault-injection function: %v", err)
		}
	})
	// CREATE TRIGGER takes no bind parameters, so the tenant is interpolated;
	// it is a UUID rendered by ids.UUID.String(), never caller text.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER lead_retention_fault BEFORE INSERT OR UPDATE ON lead
		FOR EACH ROW WHEN (NEW.workspace_id = '`+ws.String()+`'::uuid)
		EXECUTE FUNCTION retention_fault_injection()`); err != nil {
		t.Fatalf("arming the fault-injection trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP TRIGGER lead_retention_fault ON lead`); err != nil {
			t.Errorf("dropping the fault-injection trigger: %v", err)
		}
	})
}

// leadName reads one workspace's lead by owner connection — the victim
// workspace is not the harness's own tenant, so there is no bound app-pool
// context to read it through.
func leadName(t *testing.T, owner *pgx.Conn, lead ids.UUID) string {
	t.Helper()
	var name string
	if err := owner.QueryRow(context.Background(),
		`SELECT full_name FROM lead WHERE id = $1`, lead).Scan(&name); err != nil {
		t.Fatalf("reading lead %s: %v", lead, err)
	}
	return name
}

// TestRetentionReportsTheWorkspaceWhosePassFailed is the characterization:
// a tenant whose retention pass hits a database error must reach its caller as
// an error. A pass that reported success is indistinguishable from a pass that
// had nothing to do — and what it hides is subject data kept past its policy.
func TestRetentionReportsTheWorkspaceWhosePassFailed(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	victim := seedRetentionWorkspace(t, owner, "victim", false)
	victimLead := seedRetentionTenant(t, owner, victim)
	healthyLead := seedRetentionTenant(t, owner, e.WS)
	failLeadWritesFor(t, owner, victim)

	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.DiscardHandler))

	if err := svc.EvaluateWorkspace(retentionPassCtx(e.WS)); err != nil {
		t.Fatalf("the healthy tenant's pass: %v", err)
	}
	if got := leadName(t, owner, healthyLead); got != "Anonymized Lead" {
		t.Fatalf("the healthy tenant's over-age lead is %q, want the anonymized tombstone — a pass that acted on nothing would make the assertion below vacuous", got)
	}

	err := svc.EvaluateWorkspace(retentionPassCtx(victim))
	if got := leadName(t, owner, victimLead); got != "Over-age Lead" {
		t.Fatalf("the fault injection did not hold: the victim's lead is %q, so its pass never failed", got)
	}
	if err == nil {
		t.Fatal("a workspace whose retention pass failed reported success — the tenant's subject data stayed past its policy and nothing recorded that it had")
	}
}

// recordRetentionOutcome files one workspace job's outcome under the tenant its
// args name, reading the WIRE key rather than a decoded args struct — the same
// `workspace_id` every per-workspace read of river_job selects.
func recordRetentionOutcome(t *testing.T, into map[string]bool, ev *river.Event, kind string, completed bool) {
	t.Helper()
	if ev == nil || ev.Job == nil || ev.Job.Kind != kind {
		return
	}
	var args struct {
		Workspace string `json:"workspace_id"`
	}
	if err := json.Unmarshal(ev.Job.EncodedArgs, &args); err != nil {
		t.Fatalf("decoding the %s args River persisted: %v", kind, err)
	}
	if args.Workspace == "" {
		t.Fatalf("a %s row carries no workspace_id — the tenant it worked for is invisible to every per-workspace read of river_job", kind)
	}
	into[args.Workspace] = completed
}

// awaitRetentionOutcomes collects one outcome per tenant until want distinct
// workspaces have reported, or the deadline fires. No polling, no sleep.
func awaitRetentionOutcomes(ctx context.Context, t *testing.T, completed, failed <-chan *river.Event, kind string, want int) map[string]bool {
	t.Helper()
	outcomes := make(map[string]bool, want)
	for len(outcomes) < want {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out with %d of %d %s outcomes: %v", len(outcomes), want, kind, ctx.Err())
		case ev := <-completed:
			recordRetentionOutcome(t, outcomes, ev, kind, true)
		case ev := <-failed:
			recordRetentionOutcome(t, outcomes, ev, kind, false)
		}
	}
	return outcomes
}

// awaitKindsCompleted blocks until every named kind has reported one
// completion, or the deadline fires. No polling, no sleep.
func awaitKindsCompleted(ctx context.Context, t *testing.T, completed <-chan *river.Event, kinds ...string) {
	t.Helper()
	pending := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		pending[kind] = struct{}{}
	}
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting on %d of %v to complete: %v", len(pending), kinds, ctx.Err())
		case ev := <-completed:
			if ev != nil && ev.Job != nil {
				delete(pending, ev.Job.Kind)
			}
		}
	}
}

// startRetentionRunner boots a worker-role job runner whose retention
// dispatcher ticks on the given interval and returns it with its completion and
// failure channels, subscribed BEFORE Start so the RunOnStart pass's outcomes
// are never missed. The runner is stopped in cleanup.
func startRetentionRunner(t *testing.T, e *Env, interval time.Duration) (*jobs.Runner, <-chan *river.Event, <-chan *river.Event) {
	t.Helper()
	runner, err := compose.NewJobRunner(e.Pool, slog.New(slog.DiscardHandler), compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		PrivacyRetention:  compose.PrivacyRetentionConfig{Interval: interval},
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	completed, cancelCompleted := runner.SubscribeCompleted()
	t.Cleanup(cancelCompleted)
	failed, cancelFailed := runner.SubscribeFailed()
	t.Cleanup(cancelFailed)
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return runner, completed, failed
}

// TestPrivacyRetentionFansOutOneJobPerWorkspaceAndFailsOnlyTheFailedTenant is
// the converted shape: the dispatcher enqueues one row per workspace —
// ARCHIVED ones included, because archiving a tenant does not un-store the
// subject data inside it — each row names its own tenant on the wire, and the
// tenant whose pass failed is the only row that fails.
func TestPrivacyRetentionFansOutOneJobPerWorkspaceAndFailsOnlyTheFailedTenant(t *testing.T) {
	e := Setup(t)
	ApplyRiverSchema(t)
	owner := OwnerConn(t)
	victim := seedRetentionWorkspace(t, owner, "victim", false)
	archived := seedRetentionWorkspace(t, owner, "archived", true)
	victimLead := seedRetentionTenant(t, owner, victim)
	healthyLead := seedRetentionTenant(t, owner, e.WS)
	failLeadWritesFor(t, owner, victim)

	ctx := context.Background()
	_, completed, failed := startRetentionRunner(t, e, time.Hour)
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	outcomes := awaitRetentionOutcomes(waitCtx, t, completed, failed,
		compose.PrivacyRetentionWorkspaceArgs{}.Kind(), 3)

	for _, ws := range []ids.UUID{e.WS, victim, archived} {
		if _, fannedOut := outcomes[ws.String()]; !fannedOut {
			t.Errorf("no retention job ran for workspace %s — a tenant the fan-out skipped keeps its subject data past policy and no row records that it did", ws)
		}
	}
	if !outcomes[e.WS.String()] {
		t.Error("the healthy tenant's retention job failed")
	}
	if !outcomes[archived.String()] {
		t.Error("the archived tenant's retention job failed")
	}
	if outcomes[victim.String()] {
		t.Error("the tenant whose retention pass could not write reported a completed job — the failure the per-workspace row exists to record was swallowed")
	}

	if got := leadName(t, owner, healthyLead); got != "Anonymized Lead" {
		t.Errorf("the healthy tenant's over-age lead is %q, want the anonymized tombstone — its job completed without doing the pass", got)
	}
	if got := leadName(t, owner, victimLead); got != "Over-age Lead" {
		t.Errorf("the fault injection did not hold: the victim's lead is %q", got)
	}

	// The pass has to be AUDITED, not merely finished: retention is a
	// governance obligation, and the audit row plus its outbox event are what
	// evidence it. Both need the system actor and correlation id the worker
	// binds on top of the workspace, so an unprovenanced worker fails here
	// rather than in production.
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE evidence ? 'retention_action'`); n != 1 {
		t.Errorf("retention audit rows in the healthy tenant = %d, want 1", n)
	}
	var emitted int
	if err := e.Pool.QueryRow(ctx, `
		SELECT count(*) FROM event_outbox
		WHERE envelope->>'type' = 'retention.applied'
		  AND envelope->>'workspace_id' = $1
		  AND envelope->'actor'->>'id' = 'system'`, e.WS).Scan(&emitted); err != nil {
		t.Fatalf("reading the staged retention events: %v", err)
	}
	if emitted != 1 {
		t.Errorf("retention.applied events staged by the system actor = %d, want 1", emitted)
	}
}

// TestPrivacyRetentionDispatchRepeatsOnItsConfiguredInterval pins the half of
// the schedule a boot pass hides. RunOnStart fires once whatever the cadence
// is, so a dispatcher wired to a constant instead of the operator's
// --retention-interval looks identical at boot and then never runs again — a
// dead pass with every gate green. Two dispatches inside this window can only
// happen if the configured interval is what River is scheduling on.
func TestPrivacyRetentionDispatchRepeatsOnItsConfiguredInterval(t *testing.T) {
	e := Setup(t)
	ApplyRiverSchema(t)

	_, completed, _ := startRetentionRunner(t, e, 2*time.Second)
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.PrivacyRetentionArgs{}.Kind()
	dispatched := make(map[int64]struct{}, 2)
	for len(dispatched) < 2 {
		select {
		case <-waitCtx.Done():
			t.Fatalf("saw %d of 2 %s dispatches: %v — the pass fired at boot and then never again, so the operator's interval is not what schedules it",
				len(dispatched), kind, waitCtx.Err())
		case ev := <-completed:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				dispatched[ev.Job.ID] = struct{}{}
			}
		}
	}
}

// TestPrivacyRetentionWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow
// pins the omission. River accepts PeriodicInterval(0) and turns it into a
// schedule whose next run time never advances, so a runner assembled by a
// caller that never meant to run retention would dispatch the pass as fast as
// Postgres accepts an insert — contending for the default queue against
// whatever that caller actually wired the runner for, and never failing.
// Registering no schedule is the honest reading; the WORKERS still register,
// so a row an earlier boot queued is still worked rather than stranded.
func TestPrivacyRetentionWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow(t *testing.T) {
	e := Setup(t)
	ApplyRiverSchema(t)

	runner, completed, _ := startRetentionRunner(t, e, 0)
	if err := runner.Enqueue(context.Background(),
		compose.PrivacyRetentionWorkspaceArgs{Workspace: e.WS}, nil); err != nil {
		t.Fatalf("enqueueing the workspace pass an earlier boot would have left: %v", err)
	}

	// The close-date sweep is the FENCE, and it has to be: River inserts every
	// RunOnStart periodic job in one round after Start returns, so a run that
	// only waited on the hand-queued row could read the count before that round
	// had happened at all — and would then report zero however the schedule was
	// wired. Waiting for a sibling RunOnStart dispatcher to complete puts the
	// round provably in the past. The workspace pass is waited on for the other
	// half of the claim: a queued row is still worked with no schedule present.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	awaitKindsCompleted(waitCtx, t, completed,
		compose.CloseDateSweepArgs{}.Kind(), compose.PrivacyRetentionWorkspaceArgs{}.Kind())

	var dispatched int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`,
		compose.PrivacyRetentionArgs{}.Kind()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the dispatched retention passes: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched by a runner given no retention interval — a zero duration is not a cadence, and River spins on it rather than refusing it",
			dispatched, compose.PrivacyRetentionArgs{}.Kind())
	}
}
