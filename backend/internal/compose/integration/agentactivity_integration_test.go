// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// One person's view of the scheduled agent's work, against a real database.
//
// Every run and job here is BORN through runner.Store — StartRun, EnqueueJob,
// ClaimDueJobs, SaveOutcome — because a test that hand-inserts agent_run rows
// proves nothing about the rows production writes. A claim really is a claim: it
// goes through ClaimDueJobs and StartRun, exactly as the worker does.
//
// Direct SQL appears only where no writer in reach can produce the row state:
// seeding a passport (this suite has no passport writer), aging an already-real
// run's created_at into yesterday (no writer takes a start time — the column
// defaults to the database's now()), and clearing every passport to orphan its
// runs. None of the three invents a run or a job.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/agentactivity"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// activityFixture is the shared harness plus the two things this suite adds: a
// runner store to seed through, and the read under test.
type activityFixture struct {
	env   *Env
	owner *pgx.Conn
	runs  *runner.Store
	store *agentactivity.Store

	// alice and bob are two humans with a live passport each. The read must
	// separate them, so one of them has to be somebody other than Rep1.
	alice, bob                 ids.UUID
	alicePassport, bobPassport ids.PassportID

	// now and midnight come from the DATABASE clock, not Go's: the rows this
	// suite ages carry timestamps the database stamped, and a "today" computed
	// on the test host would name a different day for the few minutes either
	// side of midnight. Read once, then injected as a fixed clock.
	now, midnight time.Time
}

func setupAgentActivity(t *testing.T) *activityFixture {
	t.Helper()
	env := Setup(t)
	owner := OwnerConn(t)

	var midnight time.Time
	if err := owner.QueryRow(context.Background(),
		`SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`).Scan(&midnight); err != nil {
		t.Fatalf("reading the database's idea of today: %v", err)
	}
	midnight = midnight.UTC()
	now := midnight.Add(12 * time.Hour)

	f := &activityFixture{
		env:      env,
		owner:    owner,
		runs:     runner.NewStore(env.DB()),
		store:    agentactivity.NewStore(env.DB(), func() time.Time { return now }),
		alice:    env.Rep1,
		bob:      env.Rep2,
		now:      now,
		midnight: midnight,
	}
	f.alicePassport = f.seedPassportFor(t, f.alice, "alice overnight runner")
	f.bobPassport = f.seedPassportFor(t, f.bob, "bob overnight runner")
	return f
}

// seedPassportFor is Env.SeedPassport widened to any human: the shared harness
// binds Rep1, and this suite needs a second person's authority to prove the read
// does not hand one person the other's work.
//
// expires_at is a PARAMETER derived from this fixture's frozen clock, never
// database-clock arithmetic. The two are the same distance apart only on the day
// such a fixture is written; after that the gap grows by a day a day, and the
// suite fails on a date nobody can connect to a change.
func (f *activityFixture) seedPassportFor(t *testing.T, user ids.UUID, label string) ids.PassportID {
	t.Helper()
	id := ids.NewV7()
	if _, err := f.owner.Exec(context.Background(), `
		INSERT INTO passport (id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
		VALUES ($1, $2, $2, $3, ARRAY['read','write'], $4, $5)`,
		id, user, label, "hash-"+id.String(), f.now.Add(24*time.Hour)); err != nil {
		t.Fatalf("seeding passport %s: %v", label, err)
	}
	return ids.From[ids.PassportKind](id)
}

// spec resolves a shipped catalog entry; a name the catalog does not carry is a
// fixture bug, not a test case.
func (f *activityFixture) spec(t *testing.T, name string) runner.AgentSpec {
	t.Helper()
	s, ok := runner.SpecByName(name)
	if !ok {
		t.Fatalf("no catalog spec named %q", name)
	}
	return s
}

// nextTrigger names a fresh occurrence. One run per trigger occurrence is the
// runner's idempotency rule, so two seeds sharing a ref would be one row.
func (f *activityFixture) nextTrigger(name string) string {
	return name + ":fixture-" + ids.NewV7().String()
}

// seedRun starts one run under the given human's passport, exactly as the worker
// does after claiming a job.
func (f *activityFixture) seedRun(t *testing.T, specName string, passport ids.PassportID) ids.UUID {
	t.Helper()
	runID, created, err := f.runs.StartRun(context.Background(),
		f.spec(t, specName), f.nextTrigger(specName), passport)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !created {
		t.Fatal("StartRun found an existing occurrence; the fixture reused a trigger ref")
	}
	return runID
}

// seedQueuedJob puts one occurrence on the trigger queue with a passport already
// bound. due_at is before today's midnight so the real claim loop, which only
// takes jobs that are due, can pick it up.
func (f *activityFixture) seedQueuedJob(t *testing.T, specName string, passport ids.PassportID) {
	t.Helper()
	if err := f.runs.EnqueueJob(context.Background(), specName, f.nextTrigger(specName),
		&passport, f.midnight.Add(-time.Hour)); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
}

// claimJob is the worker's claim step: the job leaves 'queued' and its run row
// is born, which is the moment the run becomes the authority for the occurrence.
func (f *activityFixture) claimJob(t *testing.T, specName string) {
	t.Helper()
	jobs, err := f.runs.ClaimDueJobs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("claimed %d jobs, want the one this test queued", len(jobs))
	}
	job := jobs[0]
	if job.PassportID == nil {
		t.Fatal("the claimed job lost its passport; nothing could attribute its run")
	}
	if _, _, err := f.runs.StartRun(context.Background(),
		f.spec(t, specName), job.TriggerRef, *job.PassportID); err != nil {
		t.Fatalf("StartRun after claim: %v", err)
	}
}

// finish closes a run through the real outcome writer.
func (f *activityFixture) finish(t *testing.T, runID ids.UUID, final string) {
	t.Helper()
	if err := f.runs.SaveOutcome(context.Background(), runID, runner.Result{
		Outcome: runner.OutcomeCompleted,
		Final:   json.RawMessage(final),
		Steps:   []runner.Step{},
	}); err != nil {
		t.Fatalf("SaveOutcome: %v", err)
	}
}

// seedFinishedRun starts a run, closes it, and then moves created_at to the
// instant this test means. There is no writer that takes a start time — the
// column defaults to the database's now() — so the row is born real and then
// aged.
func (f *activityFixture) seedFinishedRun(t *testing.T, specName string, passport ids.PassportID, startedAt time.Time) ids.UUID {
	t.Helper()
	runID := f.seedRun(t, specName, passport)
	f.finish(t, runID, `{"summary":"seeded"}`)
	f.env.WsExec(t, `UPDATE agent_run SET created_at = $2 WHERE id = $1`, runID, startedAt)
	return runID
}

func (f *activityFixture) deleteEveryPassport(t *testing.T) {
	t.Helper()
	f.env.WsExec(t, `DELETE FROM passport`)
}

func (f *activityFixture) mine(t *testing.T, user ids.UUID) (running, recent []agentactivity.Item) {
	t.Helper()
	running, recent, err := f.store.Mine(context.Background(), user)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return running, recent
}

func TestMineReturnsOnlyRunsThisPersonAuthorized(t *testing.T) {
	f := setupAgentActivity(t)
	mine := f.seedRun(t, "morning_brief", f.alicePassport)
	theirs := f.seedRun(t, "morning_brief", f.bobPassport)

	running, _ := f.mine(t, f.alice)
	if len(running) != 1 || running[0].ID != mine {
		t.Fatalf("got %d rows %v, want exactly alice's run %s", len(running), running, mine)
	}
	for _, item := range running {
		if item.ID == theirs {
			t.Fatal("bob's run reached alice's feed")
		}
	}
}

func TestARunWhoseAuthorityWasDeletedBelongsToNobody(t *testing.T) {
	// agent_run.passport_id is nullable with ON DELETE SET NULL (core 0021), so a
	// deleted passport orphans its runs. Nobody inherits them: a NULL that reads
	// as "mine" would show one person another person's work.
	f := setupAgentActivity(t)
	f.seedRun(t, "morning_brief", f.alicePassport)
	f.deleteEveryPassport(t)

	running, _ := f.mine(t, f.alice)
	if len(running) != 0 {
		t.Fatalf("an orphaned run was attributed to alice: %v", running)
	}
}

func TestQueuedComesFromTheJobAndStopsWhenTheRunStarts(t *testing.T) {
	f := setupAgentActivity(t)
	f.seedQueuedJob(t, "morning_brief", f.alicePassport)
	running, _ := f.mine(t, f.alice)
	if len(running) != 1 || running[0].State != agentactivity.StateQueued {
		t.Fatalf("want one queued item, got %v", running)
	}

	// Claimed: the job's own status moves on and the run row is the authority.
	// One trigger occurrence must not appear twice.
	f.claimJob(t, "morning_brief")
	running, _ = f.mine(t, f.alice)
	if len(running) != 1 {
		t.Fatalf("one occurrence reported %d times: %v", len(running), running)
	}
	if running[0].State != agentactivity.StateRunning {
		t.Fatalf("after the claim the run row decides the state, got %q", running[0].State)
	}
}

func TestRecentIsBoundedToTodayAndTen(t *testing.T) {
	// An unbounded per-person run history IS the activity ledger this
	// installation does not keep, so the bound is a requirement and not a page
	// size.
	f := setupAgentActivity(t)
	for i := range 14 {
		f.seedFinishedRun(t, "morning_brief", f.alicePassport, f.now.Add(-time.Duration(i)*time.Minute))
	}
	f.seedFinishedRun(t, "morning_brief", f.alicePassport, f.now.Add(-30*time.Hour)) // yesterday

	_, recent := f.mine(t, f.alice)
	if len(recent) != 10 {
		t.Fatalf("recent returned %d items, want the 10 cap", len(recent))
	}
	for _, item := range recent {
		if item.StartedAt.Before(f.midnight) {
			t.Fatalf("a run from before today reached recent: %v", item)
		}
	}
	if !recent[0].StartedAt.After(recent[1].StartedAt) {
		t.Fatal("recent must be newest first")
	}
}

func TestASummaryIsOptional(t *testing.T) {
	// parseStep never validates that `final` carries a summary, so a completed
	// run legitimately has none. Reading it must not fail.
	f := setupAgentActivity(t)
	runID := f.seedRun(t, "morning_brief", f.alicePassport)
	f.finish(t, runID, `{"note":"no summary key here"}`)

	_, recent := f.mine(t, f.alice)
	if len(recent) != 1 || recent[0].Summary != nil {
		t.Fatalf("want one item with a nil summary, got %v", recent)
	}
}

func TestASummaryIsReadFromWhereTheRunnerWritesIt(t *testing.T) {
	// SaveOutcome stores the model's `final` object verbatim, so the summary is
	// at the TOP level of result — not under a nested "final" key. Without this
	// the optional-summary case above would also pass a read that never finds
	// one at all.
	f := setupAgentActivity(t)
	runID := f.seedRun(t, "overnight_at_risk_sweep", f.alicePassport)
	f.finish(t, runID, `{"summary":"one at-risk deal flagged"}`)

	_, recent := f.mine(t, f.alice)
	if len(recent) != 1 {
		t.Fatalf("want the one finished run, got %v", recent)
	}
	if recent[0].Summary == nil || *recent[0].Summary != "one at-risk deal flagged" {
		t.Fatalf("the run's own summary did not reach the reader: %v", recent[0].Summary)
	}
	if recent[0].State != agentactivity.StateDone {
		t.Fatalf("a completed run reads as %q, want done", recent[0].State)
	}
	if recent[0].FinishedAt == nil {
		t.Fatal("a completed run must carry the instant it finished")
	}
}

// TestADegradedRunNeverReadsAsDone pins the one state whose collapse would put a
// completion on screen that did not happen. The row is born through StartRun and
// closed through the real SaveOutcome with the outcome the runner itself writes
// when a budget guarantee fires, so the degraded row here is the production one.
func TestADegradedRunNeverReadsAsDone(t *testing.T) {
	f := setupAgentActivity(t)
	runID := f.seedRun(t, "morning_brief", f.alicePassport)
	if err := f.runs.SaveOutcome(context.Background(), runID, runner.Result{
		Outcome:       runner.OutcomeDegraded,
		DegradeReason: "step budget exhausted",
		Final:         json.RawMessage(`{"partial":true}`),
		Steps:         []runner.Step{},
	}); err != nil {
		t.Fatalf("SaveOutcome: %v", err)
	}

	_, recent := f.mine(t, f.alice)
	if len(recent) != 1 || recent[0].State != agentactivity.StateDegraded {
		t.Fatalf("want one degraded item, got %v", recent)
	}
	if recent[0].DegradeReason == nil || *recent[0].DegradeReason != "step budget exhausted" {
		t.Fatalf("the reason a run stopped early did not survive the read: %v", recent[0].DegradeReason)
	}
	if recent[0].Summary != nil {
		t.Fatalf("a degrade writes partial state, not a summary: %v", *recent[0].Summary)
	}
}

// TestAJobWithNoPassportIsAttributedToNobody covers the cron-seeded job: it
// carries passport_id NULL until an installation binds one, which is exactly the
// queued state, so the read has to refuse it rather than hand it to whoever
// asked.
func TestAJobWithNoPassportIsAttributedToNobody(t *testing.T) {
	f := setupAgentActivity(t)
	if err := f.runs.EnqueueJob(context.Background(), "morning_brief",
		f.nextTrigger("morning_brief"), nil, f.midnight.Add(-time.Hour)); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	running, _ := f.mine(t, f.alice)
	if len(running) != 0 {
		t.Fatalf("an unbound job was attributed to alice: %v", running)
	}
}
