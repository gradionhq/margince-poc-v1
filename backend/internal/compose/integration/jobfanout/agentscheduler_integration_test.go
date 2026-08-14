// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobfanout

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
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/compose/integration/jobtest"
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
func failRunnerJobWrites(t *testing.T, owner *pgx.Conn) {
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
	// Every write, not one tenant's: the table carries no workspace to key a
	// WHEN clause on (ADR-0091 §8 phase D), and the pass under test is the
	// installation's only one. The trigger drops in cleanup, so its blast
	// radius is this test.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER runner_job_write_fault_trigger
		BEFORE INSERT OR UPDATE ON runner_job
		FOR EACH ROW EXECUTE FUNCTION runner_job_write_fault()`); err != nil {
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
func seedDueRunnerJob(t *testing.T, owner *pgx.Conn, trigger string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO runner_job (agent_spec, trigger_ref, due_at)
		VALUES ('morning_brief', $1, now() - interval '1 minute')`, trigger); err != nil {
		t.Fatalf("seeding a due runner job: %v", err)
	}
}

// runnerJobOutcome reads one seeded occurrence's status and failure reason.
func runnerJobOutcome(t *testing.T, owner *pgx.Conn, trigger string) (status, lastError string) {
	t.Helper()
	var reason *string
	if err := owner.QueryRow(context.Background(),
		`SELECT status, last_error FROM runner_job WHERE trigger_ref = $1`,
		trigger).Scan(&status, &reason); err != nil {
		t.Fatalf("reading the runner job %s: %v", trigger, err)
	}
	if reason != nil {
		lastError = *reason
	}
	return status, lastError
}

// assertOccurrencesSeeded fails unless every catalog occurrence due at now
// exists. This is the half of a pass that has no other trace: an unseeded
// occurrence is simply a brief that never happens, with no row anywhere to
// notice its absence.
func assertOccurrencesSeeded(t *testing.T, owner *pgx.Conn, now time.Time) {
	t.Helper()
	for _, spec := range runner.Catalog() {
		var seeded bool
		if err := owner.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM runner_job WHERE trigger_ref = $1)`,
			spec.TriggerRef(now)).Scan(&seeded); err != nil {
			t.Fatalf("reading the seeded occurrences of %s: %v", spec.Name, err)
		}
		if !seeded {
			t.Errorf("no %s occurrence exists for %s — that agent simply never runs, and no row records it", spec.Name, now.Format(time.DateOnly))
		}
	}
}

// TestAgentSchedulerReportsAPassThatCouldNotWrite is the characterization: a
// scheduling pass that cannot write must reach its caller as an error. A pass
// that reported success over it leaves the briefs unseeded and unclaimed with
// no row saying so.
func TestAgentSchedulerReportsAPassThatCouldNotWrite(t *testing.T) {
	re := setupRunner(t)
	owner := integration.OwnerConn(t)

	const trigger = "morning_brief:write-fault"
	// Real due work, seeded before the fault is armed, so the claim has a row
	// to update and trip the trigger.
	seedDueRunnerJob(t, owner, trigger)
	failRunnerJobWrites(t, owner)

	now := afterEveryDueHour()
	if err := re.svc.Tick(schedulerPassCtx(re.wsID), now); err == nil {
		t.Fatal("a scheduling pass that could not write reported success — nothing records that the briefs were never seeded or claimed")
	}
	if status, _ := runnerJobOutcome(t, owner, trigger); status != "queued" {
		t.Fatalf("the seeded job is %s, want it untouched at queued — the fault injection did not hold, so this test proves nothing", status)
	}
}

// TestAgentSchedulerSeedsAndClaimsWhatIsDue is what survives the fan-out.
//
// It used to enqueue one row per live workspace, exclude archived tenants, and
// prove that the tenant whose writes failed was the ONLY row that failed. A
// pass is one row now (ADR-0103 §1), so what is worth pinning is the half that
// never depended on there being several: a pass seeds every occurrence due at
// its instant and claims the work already waiting — the two halves that have no
// other trace, since an unseeded occurrence is simply a brief that never
// happens.
func TestAgentSchedulerSeedsAndClaimsWhatIsDue(t *testing.T) {
	re := setupRunner(t)
	owner := integration.OwnerConn(t)

	const trigger = "morning_brief:seed-and-claim"
	seedDueRunnerJob(t, owner, trigger)

	now := afterEveryDueHour()
	if err := re.svc.Tick(schedulerPassCtx(re.wsID), now); err != nil {
		t.Fatalf("the scheduling pass: %v", err)
	}
	assertOccurrencesSeeded(t, owner, now)

	status, lastError := runnerJobOutcome(t, owner, trigger)
	if status == "queued" {
		t.Fatal("the due job is still queued — the pass reported success without claiming anything")
	}
	// Passport-less by design: the loud "no passport bound" failure on the row
	// is the cheapest honest evidence that the claim ran and the job executed.
	if status != "failed" || lastError == "" {
		t.Fatalf("the passport-less job is %s (%q), want a loud failure — the claim reached it but the execution did not", status, lastError)
	}
}

// TestAgentSchedulerDispatchRepeatsOnItsConfiguredInterval pins the half of the
// schedule a boot pass hides. RunOnStart fires once whatever the cadence is, so
// a dispatcher wired to a constant instead of the operator's --runner-interval
// looks identical at boot and then never runs again — every workspace's due
// occurrences unseeded from that moment on, with every gate green. Two
// dispatches less than jobtest.DispatchGapBound apart can only happen if a
// cadence far shorter than that flag's own default is what River is scheduling
// on.
func TestAgentSchedulerDispatchRepeatsOnItsConfiguredInterval(t *testing.T) {
	re := setupRunner(t)

	_, completed, _ := jobtest.StartTestJobRunner(t, re.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		AgentScheduler: compose.AgentSchedulerConfig{
			Interval: jobtest.DispatchInterval, Service: re.svc,
		},
	})
	// Generous compared with the gap bound: a run this slow is a sick machine,
	// and the assertion that decides the outcome is the gap below, not this.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.AgentSchedulerArgs{}.Kind()
	first, second := jobtest.AwaitTwoDispatchArrivals(waitCtx, t, completed, kind)
	if gap := second.Sub(first); gap > jobtest.DispatchGapBound {
		t.Fatalf("the two %s dispatches were %s apart, over the %s bound — the schedule is not the configured %s interval but some larger constant, and --runner-interval's own 30s default is the one that would look exactly like this",
			kind, gap, jobtest.DispatchGapBound, jobtest.DispatchInterval)
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

	jobRunner, completed, _ := jobtest.StartTestJobRunner(t, re.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		AgentScheduler:    compose.AgentSchedulerConfig{Interval: 0, Service: re.svc},
	})
	if err := jobRunner.Enqueue(context.Background(), compose.AgentSchedulerArgs{}, nil); err != nil {
		t.Fatalf("enqueueing the pass an earlier boot would have left: %v", err)
	}

	// The close-date sweep is the FENCE, and it has to be: River inserts every
	// RunOnStart periodic job in one round after Start returns, so a run that
	// only waited on the hand-queued row could read the count before that round
	// had happened at all — and would then report zero however the schedule was
	// wired. Waiting for a sibling RunOnStart dispatcher to complete puts the
	// round provably in the past. The scheduling pass is waited on for the other
	// half of the claim: a queued row is still worked with no schedule present.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	jobtest.AwaitKindsCompleted(waitCtx, t, completed,
		compose.CloseDateSweepArgs{}.Kind(), compose.AgentSchedulerArgs{}.Kind())

	var dispatched int
	if err := re.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`,
		compose.AgentSchedulerArgs{}.Kind()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the dispatched scheduling passes: %v", err)
	}
	// Exactly the one row this test queued by hand. The count used to be zero
	// because the schedule and the work were different kinds; one kind does both
	// now, so a spinning schedule shows as rows arriving ABOVE this floor.
	if dispatched != 1 {
		t.Errorf("%d %s rows exist after a runner was given no scheduler interval, want only the one queued here — a zero duration is not a cadence, and River spins on it rather than refusing it",
			dispatched, compose.AgentSchedulerArgs{}.Kind())
	}
}
