// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Surface-B agent scheduler is one job row per live tenant. Its failure
// mode is the opposite of the sweeps beside it: a tenant whose occurrence
// seeding or due-job claim hit a database error used to abandon the pass for
// EVERY LATER TENANT TOO, so one workspace's transient fault meant the morning
// brief never ran anywhere behind it in the fleet order — silently, on every
// tick, until the fault cleared.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// schedulerPassCtx is the scope the scheduler's workspace worker binds before
// it ticks: the tenant, and nothing else. Each claimed job resolves its own
// passport and mints its own correlation id inside the pass, so a suite that
// bound an actor here would be exercising provenance production never writes.
func schedulerPassCtx(ws ids.UUID) context.Context {
	return principal.WithWorkspaceID(context.Background(), ws)
}

// afterEveryDueHour is a reading late enough on its own UTC day that every
// catalog occurrence has fallen due. It is pinned rather than read off the wall
// clock because the catalog's due hours are fixed UTC hours: a suite running
// before the earliest of them would seed nothing, and its seeding assertions
// would hold vacuously for those hours of the day.
func afterEveryDueHour() time.Time {
	d := time.Now().UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), 23, 0, 0, 0, time.UTC)
}

// failRunnerJobWritesFor makes every runner_job write inside ONE tenant raise,
// leaving every other tenant's writes untouched. Both halves of a scheduling
// pass write that table — seeding INSERTs an occurrence, claiming UPDATEs it to
// running — so a BEFORE INSERT OR UPDATE trigger covers the pass whatever the
// clock says is due.
//
// It is dropped in cleanup: the integration lane resets rows between tests but
// keeps the schema, so a surviving trigger would break every later suite that
// schedules a run in this workspace.
func failRunnerJobWritesFor(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		CREATE OR REPLACE FUNCTION runner_job_write_fault() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  RAISE EXCEPTION 'runner job write fault injection';
		END $$`); err != nil {
		t.Fatalf("creating the fault-injection function: %v", err)
	}
	// Registered before the trigger is armed, not after both: a failure to arm
	// would otherwise leave the function behind, which is the leak this cleanup
	// exists to prevent. Cleanups run LIFO, so the trigger still drops first.
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP FUNCTION runner_job_write_fault()`); err != nil {
			t.Errorf("dropping the fault-injection function: %v", err)
		}
	})
	// CREATE TRIGGER takes no bind parameters, so the tenant is interpolated;
	// it is a UUID rendered by ids.UUID.String(), never caller text.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER runner_job_write_fault_trigger
		BEFORE INSERT OR UPDATE ON runner_job
		FOR EACH ROW WHEN (NEW.workspace_id = '`+ws.String()+`'::uuid)
		EXECUTE FUNCTION runner_job_write_fault()`); err != nil {
		t.Fatalf("arming the fault-injection trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP TRIGGER runner_job_write_fault_trigger ON runner_job`); err != nil {
			t.Errorf("dropping the fault-injection trigger: %v", err)
		}
	})
}

// seedDueRunnerJob plants an already-due, passport-less occurrence in ws. It
// gives a tenant real work for a pass to claim: a workspace with nothing due
// never updates a row, so neither the fault below nor the claim above it would
// have anything to act on.
//
// Passport-less is deliberate — executing it needs no model and no bound
// identity, and the loud "no passport bound" failure it lands on the row is the
// cheapest honest evidence that the claim ran and the job was executed. The
// spec is a real catalog name for the same reason: an unknown one fails one
// step earlier, before the claim's result has been read at all.
func seedDueRunnerJob(t *testing.T, owner *pgx.Conn, ws ids.UUID, trigger string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO runner_job (workspace_id, agent_spec, trigger_ref, due_at)
		VALUES ($1, 'morning_brief', $2, now() - interval '1 minute')`, ws, trigger); err != nil {
		t.Fatalf("seeding a due runner job in %s: %v", ws, err)
	}
}

// runnerJobOutcome reads one seeded occurrence's status and failure reason.
func runnerJobOutcome(t *testing.T, owner *pgx.Conn, ws ids.UUID, trigger string) (status, lastError string) {
	t.Helper()
	var reason *string
	if err := owner.QueryRow(context.Background(),
		`SELECT status, last_error FROM runner_job WHERE workspace_id = $1 AND trigger_ref = $2`,
		ws, trigger).Scan(&status, &reason); err != nil {
		t.Fatalf("reading the runner job %s in %s: %v", trigger, ws, err)
	}
	if reason != nil {
		lastError = *reason
	}
	return status, lastError
}

// assertOccurrencesSeeded fails unless every catalog occurrence due at now
// exists in ws. This is the half of a pass that has no other trace: an
// unseeded occurrence is simply a brief that never happens, with no row
// anywhere to notice its absence.
func assertOccurrencesSeeded(t *testing.T, owner *pgx.Conn, ws ids.UUID, now time.Time) {
	t.Helper()
	for _, spec := range runner.Catalog() {
		var seeded bool
		if err := owner.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM runner_job WHERE workspace_id = $1 AND trigger_ref = $2)`,
			ws, spec.TriggerRef(now)).Scan(&seeded); err != nil {
			t.Fatalf("reading the seeded occurrences of %s in %s: %v", spec.Name, ws, err)
		}
		if !seeded {
			t.Errorf("workspace %s has no %s occurrence for %s — that agent simply never runs there, and no row records it", ws, spec.Name, now.Format(time.DateOnly))
		}
	}
}

// TestAgentSchedulerReportsTheWorkspaceWhoseSchedulingFailed is the
// characterization: a tenant whose scheduling pass cannot write must reach its
// caller as an error, and must cost that tenant its pass and nothing more. A
// pass that reported success over it leaves the tenant's briefs unseeded and
// unclaimed with no row saying so; a pass that abandoned the fleet there does
// the same to every workspace behind it.
func TestAgentSchedulerReportsTheWorkspaceWhoseSchedulingFailed(t *testing.T) {
	re := setupRunner(t)
	owner := OwnerConn(t)
	healthy := seedExtraWorkspace(t, owner, "scheduler-healthy", false)

	const trigger = "morning_brief:fleet-isolation"
	// Both tenants get real due work, seeded before the fault is armed: the
	// victim's so its claim has a row to update and trip the trigger, the
	// healthy tenant's so the claim assertion below rests on a claim that
	// actually happened.
	seedDueRunnerJob(t, owner, re.wsID, trigger)
	seedDueRunnerJob(t, owner, healthy, trigger)
	failRunnerJobWritesFor(t, owner, re.wsID)

	now := afterEveryDueHour()
	if err := re.svc.TickWorkspace(schedulerPassCtx(re.wsID), now); err == nil {
		t.Fatal("a workspace whose scheduling pass could not write reported success — nothing records that its briefs were never seeded or claimed")
	}
	if status, _ := runnerJobOutcome(t, owner, re.wsID, trigger); status != "queued" {
		t.Fatalf("the faulted tenant's job is %s, want it untouched at queued — the fault injection did not hold, so the isolation claim below rests on nothing", status)
	}

	// The fault is one tenant's, and the pass now takes the tenant it is given:
	// with the trigger still armed, the healthy tenant seeds its own catalog
	// occurrences and claims its own due job.
	if err := re.svc.TickWorkspace(schedulerPassCtx(healthy), now); err != nil {
		t.Fatalf("the healthy tenant's pass, while the victim's is faulted: %v", err)
	}
	assertOccurrencesSeeded(t, owner, healthy, now)
	status, lastError := runnerJobOutcome(t, owner, healthy, trigger)
	if status == "queued" {
		t.Fatalf("workspace %s was never scheduled: its due job is still queued, so the pass reported success without claiming anything", healthy)
	}
	if status != "failed" || lastError == "" {
		t.Fatalf("the healthy tenant's passport-less job is %s (%q), want a loud failure — the claim reached it but the execution did not", status, lastError)
	}
}

// TestAgentSchedulerFansOutOneJobPerLiveWorkspaceAndFailsOnlyTheFailedTenant is
// the converted shape: the dispatcher enqueues one row per LIVE workspace —
// archived ones excluded, because an archived tenant has nobody to read a
// morning brief and seeding one would spend model budget on a report no one
// opens — each row names its own tenant on the wire, and the tenant whose
// writes fail is the only row that fails.
func TestAgentSchedulerFansOutOneJobPerLiveWorkspaceAndFailsOnlyTheFailedTenant(t *testing.T) {
	re := setupRunner(t)
	owner := OwnerConn(t)
	healthy := seedExtraWorkspace(t, owner, "scheduler-healthy", false)
	archived := seedExtraWorkspace(t, owner, "scheduler-archived", true)

	const trigger = "morning_brief:fanout"
	seedDueRunnerJob(t, owner, re.wsID, trigger)
	seedDueRunnerJob(t, owner, healthy, trigger)
	// Permanent, not transient: this kind takes a single attempt, and a fault
	// that healed would let the tenant complete and read as green — the exact
	// outcome this test denies.
	failRunnerJobWritesFor(t, owner, re.wsID)

	now := afterEveryDueHour()
	_, completed, failed := startTestJobRunner(t, re.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		AgentScheduler: compose.AgentSchedulerConfig{
			Interval: time.Hour, Service: re.svc, Now: func() time.Time { return now },
		},
	})
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.AgentSchedulerWorkspaceArgs{}.Kind()
	outcomes := awaitWorkspaceJobOutcomes(waitCtx, t, completed, failed, kind, 2)

	if _, fannedOut := outcomes[healthy.String()]; !fannedOut {
		t.Errorf("no scheduling pass ran for workspace %s — a tenant the fan-out skipped gets no brief and no at-risk sweep, and no row records that it did not", healthy)
	}
	if !outcomes[healthy.String()] {
		t.Error("the healthy tenant's scheduling pass failed")
	}
	if outcomes[re.wsID.String()] {
		t.Error("the tenant whose writes could not land reported a completed job — the failure the per-workspace row exists to record was swallowed")
	}
	// The pass is only worth a row if it did the work: the healthy tenant's own
	// occurrences are seeded and its due job executed, through the job path
	// rather than a direct call.
	assertOccurrencesSeeded(t, owner, healthy, now)
	if status, _ := runnerJobOutcome(t, owner, healthy, trigger); status != "failed" {
		t.Errorf("the healthy tenant's passport-less job is %s, want the loud failure its execution lands — the pass completed without claiming anything", status)
	}

	// The archived tenant must have no row at all. This count is fenced on the
	// two outcomes above rather than read early: the fan-out is ONE atomic
	// InsertMany, so any child reporting proves that insert committed — and it
	// carried every workspace the dispatcher enumerated.
	var dispatched int
	if err := re.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'workspace_id' = $2`,
		kind, archived.String()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the archived tenant's scheduling jobs: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched for an ARCHIVED workspace — seeding a brief nobody will read spends model budget on work nobody wants", dispatched, kind)
	}
}

// TestAgentSchedulerDispatchRepeatsOnItsConfiguredInterval pins the half of the
// schedule a boot pass hides. RunOnStart fires once whatever the cadence is, so
// a dispatcher wired to a constant instead of the operator's --runner-interval
// looks identical at boot and then never runs again — every workspace's due
// occurrences unseeded from that moment on, with every gate green. Two
// dispatches less than dispatchGapBound apart can only happen if a cadence far
// shorter than that flag's own default is what River is scheduling on.
func TestAgentSchedulerDispatchRepeatsOnItsConfiguredInterval(t *testing.T) {
	re := setupRunner(t)

	_, completed, _ := startTestJobRunner(t, re.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		AgentScheduler: compose.AgentSchedulerConfig{
			Interval: dispatchInterval, Service: re.svc,
		},
	})
	// Generous compared with the gap bound: a run this slow is a sick machine,
	// and the assertion that decides the outcome is the gap below, not this.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.AgentSchedulerArgs{}.Kind()
	first, second := awaitTwoDispatchArrivals(waitCtx, t, completed, kind)
	if gap := second.Sub(first); gap > dispatchGapBound {
		t.Fatalf("the two %s dispatches were %s apart, over the %s bound — the schedule is not the configured %s interval but some larger constant, and --runner-interval's own 30s default is the one that would look exactly like this",
			kind, gap, dispatchGapBound, dispatchInterval)
	}
}

// TestAgentSchedulerWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow
// pins the omission. River accepts PeriodicInterval(0) and turns it into a
// schedule whose next run time never advances, so a runner assembled by a
// caller that never meant to schedule agents would fan the whole fleet out as
// fast as Postgres accepts an insert. Registering no schedule is the honest
// reading; the WORKERS still register, so a row an earlier boot queued is still
// worked rather than stranded.
func TestAgentSchedulerWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow(t *testing.T) {
	re := setupRunner(t)

	jobRunner, completed, _ := startTestJobRunner(t, re.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		AgentScheduler:    compose.AgentSchedulerConfig{Interval: 0, Service: re.svc},
	})
	if err := jobRunner.Enqueue(context.Background(),
		compose.AgentSchedulerWorkspaceArgs{Workspace: re.wsID}, nil); err != nil {
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
		compose.CloseDateSweepArgs{}.Kind(), compose.AgentSchedulerWorkspaceArgs{}.Kind())

	var dispatched int
	if err := re.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`,
		compose.AgentSchedulerArgs{}.Kind()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the dispatched scheduling passes: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched by a runner given no scheduler interval — a zero duration is not a cadence, and River spins on it rather than refusing it",
			dispatched, compose.AgentSchedulerArgs{}.Kind())
	}
}
