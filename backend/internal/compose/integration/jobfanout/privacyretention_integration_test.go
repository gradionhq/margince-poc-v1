// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobfanout

// The GDPR retention pass is one job row per tenant. A workspace whose pass
// fails must say so — a retention pass that failed and reported success leaves
// subject data stored past its policy with nothing recording that it happened,
// which is the one failure mode this engine exists to prevent.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/compose/integration/jobtest"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedRetentionTenant plants the lead/unconverted anonymize policy and one
// over-age unconverted lead in ws, through the owner connection so a workspace
// other than the harness's own can be given a due record.
// seedRetentionPolicy installs the lead/unconverted policy ONCE.
//
// It used to be seeded per tenant. `retention_policy_unique` no longer carries
// a workspace (ADR-0091 §8 phase B), so a policy for an (object_type, category)
// is the installation's — seeding it twice is a conflict, not a second policy.
// The pass this suite exercises is still per workspace; what each tenant needs
// of its own is the over-age record below, not a copy of the rule.
func seedRetentionPolicy(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO retention_policy (object_type, category, retain_days, action)
		VALUES ('lead', 'unconverted', 365, 'anonymize')`); err != nil {
		t.Fatalf("seeding the retention policy: %v", err)
	}
}

// seedOverageLead gives one tenant a lead old enough for the policy to reach.
func seedOverageLead(t *testing.T, owner *pgx.Conn, ws ids.UUID) ids.UUID {
	t.Helper()
	return integration.SeedIDRow(t, owner, `
		INSERT INTO lead (id, full_name, status, source, captured_by, created_at)
		VALUES ($1, 'Over-age Lead', 'new', 'manual', 'human:x', now() - interval '400 days')`)
}

// failLeadWrites makes every lead write raise.
//
// A trigger is the fault seam because nothing in the fixture data can produce
// this failure: the retention pass fails on SQL errors, not on record shapes,
// so a test that only varied the seed could never reach the path where a pass
// fails. It is dropped in cleanup — the integration lane resets rows between
// tests but keeps the schema, so a surviving trigger would fail every later
// suite that writes a lead.
func failLeadWrites(t *testing.T, owner *pgx.Conn) {
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
	// Every lead write, not one tenant's: the pass under test is the
	// installation's only one, so a WHEN clause would select the same rows the
	// trigger already reaches.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER lead_retention_fault BEFORE INSERT OR UPDATE ON lead
		FOR EACH ROW EXECUTE FUNCTION retention_fault_injection()`); err != nil {
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

// TestRetentionReportsAPassThatCouldNotWrite is the characterization: a
// retention pass that hits a database error must reach its caller as an error.
// A pass that reported success is indistinguishable from a pass that had
// nothing to do — and what it hides is subject data kept past its policy.
func TestRetentionReportsAPassThatCouldNotWrite(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	seedRetentionPolicy(t, owner, e.WS)
	firstLead := seedOverageLead(t, owner, e.WS)

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.DiscardHandler))

	// A healthy pass first: without it the assertion below could not tell a
	// held fault from a pass that never had anything to act on.
	if err := svc.EvaluateInstallation(integration.RetentionPassCtx(e.WS)); err != nil {
		t.Fatalf("the pass before any fault was injected: %v", err)
	}
	if got := leadName(t, owner, firstLead); got != "Anonymized Lead" {
		t.Fatalf("the over-age lead is %q, want the anonymized tombstone — the pass acted on nothing", got)
	}

	// A fresh over-age lead for the faulted pass to reach, seeded AFTER the
	// healthy pass (which would otherwise have anonymized it too) and BEFORE
	// the trigger, which refuses inserts as well as updates.
	secondLead := seedOverageLead(t, owner, e.WS)
	failLeadWrites(t, owner)
	err := svc.EvaluateInstallation(integration.RetentionPassCtx(e.WS))
	if got := leadName(t, owner, secondLead); got != "Over-age Lead" {
		t.Fatalf("the fault injection did not hold: the lead is %q, so the pass never failed", got)
	}
	if err == nil {
		t.Fatal("a retention pass that failed reported success — subject data stayed past its policy and nothing recorded that it had")
	}
}

// startRetentionRunner boots a job runner whose retention dispatcher ticks on
// the given interval, and returns it with the shared fan-out harness's
// completion and failure channels.
func startRetentionRunner(t *testing.T, e *integration.Env, interval time.Duration) (*jobs.Runner, <-chan *river.Event, <-chan *river.Event) {
	t.Helper()
	return jobtest.StartTestJobRunner(t, e.Pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		PrivacyRetention:  compose.PrivacyRetentionConfig{Interval: interval},
	})
}

// TestPrivacyRetentionRunsThePassAndRecordsAFailureAsAFailedRow is what
// survives the fan-out.
//
// It used to enqueue one row per workspace — archived ones included, because
// archiving a tenant does not un-store the subject data inside it — and prove
// that the tenant whose pass failed was the only row that failed. A pass is one
// row now (ADR-0103 §1), and the archived-tenant obligation is the pass's own:
// it acts on every row past policy, whatever workspace once held it. What is
// left to pin is that the scheduled pass DOES the work, and that a pass which
// cannot reports as a failed row rather than a completed one.
func TestPrivacyRetentionRunsThePassAndRecordsAFailureAsAFailedRow(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	owner := integration.OwnerConn(t)
	seedRetentionPolicy(t, owner, e.WS)
	overage := seedOverageLead(t, owner, e.WS)

	ctx := context.Background()
	_, completed, failed := startRetentionRunner(t, e, time.Hour)
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	kind := compose.PrivacyRetentionArgs{}.Kind()
	if outcome := jobtest.AwaitKindOutcome(waitCtx, t, completed, failed, kind); !outcome {
		t.Fatal("the scheduled retention pass failed with nothing faulted")
	}
	if got := leadName(t, owner, overage); got != "Anonymized Lead" {
		t.Errorf("the over-age lead is %q after a completed retention job, want the anonymized tombstone — the job reported success without doing the pass", got)
	}
}

// TestPrivacyRetentionRecordsAFailedPassAsAFailedRow is the other half, and the
// half that has no other trace: a pass that could not run must leave a FAILED
// row. One reporting success is indistinguishable from one that found nothing
// due, and what it hides is subject data kept past its policy with a green job
// row over it.
func TestPrivacyRetentionRecordsAFailedPassAsAFailedRow(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	owner := integration.OwnerConn(t)
	seedRetentionPolicy(t, owner, e.WS)
	seedOverageLead(t, owner, e.WS)
	// Armed before the runner starts, and permanent: a fault that healed would
	// let a later attempt complete and record the pass as green.
	failLeadWrites(t, owner)

	_, completed, failed := startRetentionRunner(t, e, time.Hour)
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if outcome := jobtest.AwaitKindOutcome(waitCtx, t, completed, failed,
		compose.PrivacyRetentionArgs{}.Kind()); outcome {
		t.Error("the pass whose writes could not land reported a completed job — the failure the row exists to record was swallowed")
	}
}

// TestPrivacyRetentionDispatchRepeatsOnItsConfiguredInterval pins the half of
// the schedule a boot pass hides. RunOnStart fires once whatever the cadence
// is, so a dispatcher wired to a constant instead of the operator's
// --retention-interval looks identical at boot and then never runs again — a
// dead storage-limitation obligation with every gate green. Two dispatches less
// than jobtest.DispatchGapBound apart can only happen if a cadence far shorter
// than any constant in reach is what River is scheduling on.
func TestPrivacyRetentionDispatchRepeatsOnItsConfiguredInterval(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)

	_, completed, _ := startRetentionRunner(t, e, jobtest.DispatchInterval)
	// Generous compared with the gap bound: a run this slow is a sick machine,
	// and the assertion that decides the outcome is the gap below, not this.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.PrivacyRetentionArgs{}.Kind()
	first, second := jobtest.AwaitTwoDispatchArrivals(waitCtx, t, completed, kind)
	if gap := second.Sub(first); gap > jobtest.DispatchGapBound {
		t.Fatalf("the two %s dispatches were %s apart, over the %s bound — the schedule is not the configured %s interval but some larger constant, and the gmail_sync dispatcher's declared 30s scan is the one that would look exactly like this",
			kind, gap, jobtest.DispatchGapBound, jobtest.DispatchInterval)
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
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)

	runner, completed, _ := startRetentionRunner(t, e, 0)
	if err := runner.Enqueue(context.Background(),
		compose.PrivacyRetentionArgs{}, nil); err != nil {
		t.Fatalf("enqueueing the pass an earlier boot would have left: %v", err)
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
	jobtest.AwaitKindsCompleted(waitCtx, t, completed,
		compose.CloseDateSweepArgs{}.Kind(), compose.PrivacyRetentionArgs{}.Kind())

	var dispatched int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`,
		compose.PrivacyRetentionArgs{}.Kind()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the dispatched retention passes: %v", err)
	}
	// Exactly the one row this test queued by hand: the schedule and the work
	// are one kind now, so a spinning schedule shows as rows above this floor.
	if dispatched != 1 {
		t.Errorf("%d %s rows exist after a runner was given no retention interval, want only the one queued here — a zero duration is not a cadence, and River spins on it rather than refusing it",
			dispatched, compose.PrivacyRetentionArgs{}.Kind())
	}
}
