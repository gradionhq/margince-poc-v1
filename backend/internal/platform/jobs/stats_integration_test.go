// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobs_test

// Real-Postgres proof of the two reads behind the job surfaces. The SQL is
// the whole of the behaviour here — a fake pool would prove the fixture,
// not the query — so every case below seeds raw river_job rows and reads
// them back through the exported entry point.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seed is one raw river_job row. Written directly rather than through
// River's client because these tests need states, timestamps and stored
// error text that a real insert cannot produce on demand.
type seed struct {
	Kind      string
	Queue     string   // empty means "default"
	State     string   // river_job_state
	Workspace ids.UUID // the zero value writes NO workspace_id key at all — a dispatcher
	// Connection is the fan-out unit of the three per-connection dispatchers.
	// The zero value writes no connection_id key, which is what a
	// workspace-grained child's args look like.
	Connection ids.UUID
	Tags       []string
	CreatedAt  time.Time // the zero value means now
	Scheduled  time.Time // the zero value means CreatedAt
	Attempt    int
}

// finalizedStates are the states river_job's finalized_or_finalized_at_null
// CHECK requires a finalized_at for. A fixture that omits it is refused by
// Postgres, not by the assertion, so the helper stamps it rather than
// leaving every caller to remember.
var finalizedStates = map[string]bool{"completed": true, "cancelled": true, "discarded": true}

// seedJob writes one raw river_job row and fails the test if Postgres
// refuses it.
func seedJob(ctx context.Context, t *testing.T, pool *pgxpool.Pool, s seed) {
	t.Helper()

	queue := s.Queue
	if queue == "" {
		queue = "default"
	}
	createdAt := s.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	scheduledAt := s.Scheduled
	if scheduledAt.IsZero() {
		scheduledAt = createdAt
	}

	// A zero workspace writes no key at all, which is exactly what a
	// dispatcher's args look like on the wire.
	args := map[string]any{}
	if s.Workspace != (ids.UUID{}) {
		args["workspace_id"] = s.Workspace.String()
	}
	if s.Connection != (ids.UUID{}) {
		args["connection_id"] = s.Connection.String()
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding fixture args: %v", err)
	}

	// river_job.tags is NOT NULL, and a nil Go slice binds as NULL rather
	// than as an empty array — an untagged row is the common case here, so
	// the default belongs in the helper.
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}

	var finalizedAt *time.Time
	if finalizedStates[s.State] {
		finalized := createdAt
		finalizedAt = &finalized
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job
		    (state, kind, queue, args, tags, errors, max_attempts, attempt,
		     created_at, scheduled_at, finalized_at)
		VALUES ($1::river_job_state, $2, $3, $4::jsonb, $5::varchar(255)[],
		        $6::jsonb[], $7, $8, $9, $10, $11)`,
		s.State, s.Kind, queue, encodedArgs, tags, [][]byte{},
		3, s.Attempt, createdAt, scheduledAt, finalizedAt); err != nil {
		t.Fatalf("seeding a %s %s row: %v", s.State, s.Kind, err)
	}
}

// count answers one group's Count, or 0 when the group is absent — the two
// are different claims, so the absent case is asserted explicitly where it
// matters rather than being folded in here.
func count(snap jobs.Snapshot, kind, state, workspace string) int64 {
	var total int64
	for _, r := range snap.Rows {
		if r.Kind == kind && r.State == state && r.WorkspaceID == workspace {
			total += r.Count
		}
	}
	return total
}

func sweepFor(snap jobs.Snapshot, kind string) (jobs.SweepPass, bool) {
	for _, s := range snap.Sweeps {
		if s.Kind == kind {
			return s, true
		}
	}
	return jobs.SweepPass{}, false
}

func unitFor(snap jobs.Snapshot, kind string) (jobs.SweepUnit, bool) {
	for _, u := range snap.Units {
		if u.Kind == kind {
			return u, true
		}
	}
	return jobs.SweepUnit{}, false
}

// TestStatsCountsLiveWorkAndNamesTheWorkspaceThatOwnsIt proves the two
// properties every reader downstream stands on: a tenant job reports its
// workspace, and a dispatcher reports the empty string rather than being
// silently grouped with one.
func TestStatsCountsLiveWorkAndNamesTheWorkspaceThatOwnsIt(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	ws := ids.NewV7()

	seedJob(ctx, t, pool, seed{Kind: "tenant_pass", State: "available", Workspace: ws})
	seedJob(ctx, t, pool, seed{Kind: "tenant_pass", State: "discarded", Workspace: ws})
	seedJob(ctx, t, pool, seed{Kind: "the_dispatcher", State: "available"}) // no workspace

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if got := count(snap, "tenant_pass", "available", ws.String()); got != 1 {
		t.Errorf("tenant available = %d, want 1", got)
	}
	if got := count(snap, "tenant_pass", "discarded", ws.String()); got != 1 {
		t.Errorf("tenant discarded = %d, want 1: a failed pass must stay visible", got)
	}
	if got := count(snap, "the_dispatcher", "available", ""); got != 1 {
		t.Errorf("dispatcher under empty workspace = %d, want 1: a null args workspace "+
			"means a dispatcher, and the reader must be able to say so", got)
	}
}

// TestStatsExcludesCompletedWorkFromTheRuntimeGauges — a finished job is
// history, not queue depth. Without this the gauges only fall when River's
// cleaner runs.
func TestStatsExcludesCompletedWorkFromTheRuntimeGauges(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	seedJob(ctx, t, pool, seed{Kind: "done", State: "completed"})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got := count(snap, "done", "completed", ""); got != 0 {
		t.Errorf("a completed row reached the runtime gauges: count = %d", got)
	}
}

// TestStatsMeasuresTheAgeOfRunnableWorkOnlyAndInTheDatabasesOwnClock — the
// gauge answers "how long has runnable work been waiting", so a job
// scheduled for the future contributes nothing (it is not late) while one
// whose scheduled time has passed does (a stopped scheduler is precisely
// what this gauge exists to catch). The age is computed by Postgres from
// its own now(): subtracting a database timestamp from the app clock is a
// live intermittent flake elsewhere in this tree.
func TestStatsMeasuresTheAgeOfRunnableWorkOnlyAndInTheDatabasesOwnClock(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	anHourAgo := time.Now().Add(-time.Hour)

	seedJob(ctx, t, pool, seed{
		Kind: "overdue", Queue: "waiting", State: "scheduled",
		CreatedAt: anHourAgo, Scheduled: anHourAgo,
	})
	seedJob(ctx, t, pool, seed{
		Kind: "not_yet", Queue: "future", State: "scheduled",
		CreatedAt: anHourAgo, Scheduled: time.Now().Add(time.Hour),
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	var overdue, notYet *float64
	var sawNotYet bool
	for _, r := range snap.Rows {
		switch r.Kind {
		case "overdue":
			overdue = r.OldestRunnableAgeSeconds
		case "not_yet":
			notYet, sawNotYet = r.OldestRunnableAgeSeconds, true
		}
	}
	if overdue == nil {
		t.Fatal("an hour-overdue scheduled job reported no age at all")
	}
	if *overdue < 3000 {
		t.Errorf("an hour-overdue scheduled job reported age %.0fs: a scheduler that "+
			"stopped must not read as a healthy queue on the gauge meant to catch it", *overdue)
	}
	if !sawNotYet {
		t.Fatal("the future-scheduled row is missing from the snapshot entirely; it is " +
			"queued work and must still be counted, only not aged")
	}
	if notYet != nil {
		t.Errorf("a job scheduled for the future reported age %.0fs; it is not runnable, so "+
			"there is no oldest-runnable job to report and NULL is the honest answer", *notYet)
	}
}

// TestAPresentButEmptyWorkspaceIsNotReportedAsADispatcher — river_job has
// no constraint forcing the workspace key to be absent rather than empty,
// and the reader's whole invariant is that an empty label means dispatcher.
// The two must be distinguishable in the snapshot or the renderer cannot
// tell them apart either.
func TestAPresentButEmptyWorkspaceIsNotReportedAsADispatcher(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()

	seedJob(ctx, t, pool, seed{Kind: "real_dispatcher", State: "available"})
	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job (state, kind, queue, args, tags, errors, max_attempts,
		                       attempt, created_at, scheduled_at)
		VALUES ('available', 'malformed_row', 'default', '{"workspace_id": ""}'::jsonb,
		        '{}'::varchar(255)[], '{}'::jsonb[], 3, 0, now(), now())`); err != nil {
		t.Fatalf("seeding a present-but-empty workspace row: %v", err)
	}

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	var sawDispatcher, sawMalformed bool
	for _, r := range snap.Rows {
		switch r.Kind {
		case "real_dispatcher":
			sawDispatcher = true
			if !r.Untenanted {
				t.Error("a row with no workspace key at all was not reported as untenanted")
			}
		case "malformed_row":
			sawMalformed = true
			if r.Untenanted {
				t.Error("a row whose workspace key is PRESENT but empty was reported as a " +
					"dispatcher; the empty label would then mean two different things")
			}
			if r.WorkspaceID != "" {
				t.Errorf("WorkspaceID = %q, want the stored empty string verbatim", r.WorkspaceID)
			}
		}
	}
	// Without these, a snapshot missing either row passes every assertion
	// above by never entering its arm — the distinction would be untested
	// while the test reported green.
	if !sawDispatcher {
		t.Error("the untenanted dispatcher row never reached the snapshot")
	}
	if !sawMalformed {
		t.Error("the present-but-empty row never reached the snapshot")
	}
}

// TestASweepChildWithAnEmptyWorkspaceIsNotCountedAsATenant — the sweep pair
// answers "how much of the fleet did this pass cover", so a malformed row
// admitted as a workspace of its own inflates the fleet with a phantom
// tenant. It is excluded by the same test the state read uses.
func TestASweepChildWithAnEmptyWorkspaceIsNotCountedAsATenant(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	tenant := ids.NewV7()

	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "completed",
		Workspace: tenant, Tags: []string{jobs.SweepTag},
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job (state, kind, queue, args, tags, errors, max_attempts,
		                       attempt, created_at, scheduled_at)
		VALUES ('available', 'sweep_child', 'default', '{"workspace_id": ""}'::jsonb,
		        ARRAY['sweep']::varchar(255)[], '{}'::jsonb[], 3, 0, now(), now())`); err != nil {
		t.Fatalf("seeding a tagged present-but-empty workspace row: %v", err)
	}

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	pass, ok := sweepFor(snap, "sweep_child")
	if !ok {
		t.Fatal("the real tagged child is missing from the sweep read")
	}
	if pass.Workspaces != 1 {
		t.Errorf("Workspaces = %d, want 1: a row whose workspace key is present but empty "+
			"is malformed, not a tenant this pass covered", pass.Workspaces)
	}
}

// TestSweepCountsEachWorkspacesLatestOutcomeAndSurvivesADeduplicatedRetry
// seeds the exact shape a rescued dispatcher produces — one workspace whose
// older child is still live and so was deduplicated out of the newer
// fan-out — and asserts the fleet is still reported whole. A fixture that
// gives every row the same timestamp proves the fixture, not the query.
func TestSweepCountsEachWorkspacesLatestOutcomeAndSurvivesADeduplicatedRetry(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	older, newer := time.Now().Add(-2*time.Hour), time.Now().Add(-1*time.Hour)
	wsA, wsB, wsC, wsD := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()

	// A: still retryable from the EARLIER fan-out — deduplicated out of the
	// newer one, so its only row carries the old timestamp.
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "retryable",
		Workspace: wsA, Tags: []string{jobs.SweepTag}, CreatedAt: older,
	})
	// B: succeeded earlier, ran again in the newer pass and died.
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "completed",
		Workspace: wsB, Tags: []string{jobs.SweepTag}, CreatedAt: older,
	})
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "discarded",
		Workspace: wsB, Tags: []string{jobs.SweepTag}, CreatedAt: newer,
	})
	// C: fine, newest pass only.
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "completed",
		Workspace: wsC, Tags: []string{jobs.SweepTag}, CreatedAt: newer,
	})
	// D: cancelled, which counts as failed for the same reason discarded
	// does — a cancelled pass did not run, whatever the reason.
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "cancelled",
		Workspace: wsD, Tags: []string{jobs.SweepTag}, CreatedAt: newer,
	})
	// A hand-triggered job of the same kind, carrying no sweep tag.
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "available",
		Workspace: ids.NewV7(), CreatedAt: newer,
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	pass, ok := sweepFor(snap, "sweep_child")
	if !ok {
		t.Fatal("no sweep reported for a tagged kind")
	}
	if pass.Workspaces != 4 {
		t.Errorf("Workspaces = %d, want 4: a workspace deduplicated out of the newest "+
			"fan-out is still covered, and an untagged row is not a fleet pass at all",
			pass.Workspaces)
	}
	if pass.Failed != 2 {
		t.Errorf("Failed = %d, want 2: only B's LATEST outcome counts, and B succeeded "+
			"before it died; D was cancelled, which is also work that did not happen",
			pass.Failed)
	}
}

// TestTheUnitPairSeesAFailedConnectionTheWorkspacePairMasks is the whole
// reason the second pair exists, seeded as the exact shape that hides the
// failure: ONE workspace, TWO connections, the broken one failing FIRST and
// the healthy one completing after it.
//
// Read per workspace, that workspace's most recent child is the successful
// one and the pass looks clean. Read per connection, one unit of two is dead.
// Both readings are asserted here, not just the new one — the masking is a
// documented property of the workspace pair, and a change that silently
// "fixed" it there would break what that pair is for (a batch has no
// identity in this table) while this test still passed on the new half alone.
//
// telegram_poll is used rather than a made-up kind because the unit read is
// derived from the contract: only kinds a dispatcher declares it fans out to
// per connection are read at all, so a fixture kind would prove a query that
// never runs.
func TestTheUnitPairSeesAFailedConnectionTheWorkspacePairMasks(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	earlier, later := time.Now().Add(-2*time.Hour), time.Now().Add(-1*time.Hour)
	ws := ids.NewV7()
	broken, healthy := ids.NewV7(), ids.NewV7()

	seedJob(ctx, t, pool, seed{
		Kind: "telegram_poll", State: "discarded",
		Workspace: ws, Connection: broken, Tags: []string{jobs.SweepTag}, CreatedAt: earlier,
	})
	seedJob(ctx, t, pool, seed{
		Kind: "telegram_poll", State: "completed",
		Workspace: ws, Connection: healthy, Tags: []string{jobs.SweepTag}, CreatedAt: later,
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	pass, ok := sweepFor(snap, "telegram_poll")
	if !ok {
		t.Fatal("no per-workspace sweep reported for a tagged kind")
	}
	if pass.Workspaces != 1 || pass.Failed != 0 {
		t.Errorf("workspace pair = %d/%d covered/failed, want 1/0 — this pair counts one workspace "+
			"once and reads its LATEST child, which is why the failure is invisible here",
			pass.Workspaces, pass.Failed)
	}

	unit, ok := unitFor(snap, "telegram_poll")
	if !ok {
		t.Fatal("no per-unit sweep reported for a kind whose dispatcher fans out per connection")
	}
	if unit.Unit != jobs.FanOutConnection {
		t.Errorf("Unit = %d, want FanOutConnection — the label an alert reads the grain off", unit.Unit)
	}
	if unit.Units != 2 {
		t.Errorf("Units = %d, want 2: one workspace holding two connections is two units", unit.Units)
	}
	if unit.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — the broken connection, which the workspace pair reported as zero", unit.Failed)
	}
}

// TestTheUnitPairIgnoresAnUntaggedRowAndAWorkspaceGrainedKind holds the two
// boundaries of the new read. An untagged row of a per-connection kind is a
// job someone triggered by hand, not one connection's share of a fleet pass;
// and a kind whose declared unit IS the workspace must not appear here at all,
// or its number would be published twice and an operator summing the pairs
// would double-count the fleet.
func TestTheUnitPairIgnoresAnUntaggedRowAndAWorkspaceGrainedKind(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	ws := ids.NewV7()

	seedJob(ctx, t, pool, seed{
		Kind: "telegram_poll", State: "discarded",
		Workspace: ws, Connection: ids.NewV7(),
	})
	seedJob(ctx, t, pool, seed{
		Kind: "capture_digest_workspace", State: "discarded",
		Workspace: ws, Tags: []string{jobs.SweepTag},
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if unit, ok := unitFor(snap, "telegram_poll"); ok {
		t.Errorf("an untagged row produced a unit series (%+v); a hand-triggered poll is not one "+
			"connection's share of a fleet pass", unit)
	}
	if unit, ok := unitFor(snap, "capture_digest_workspace"); ok {
		t.Errorf("a workspace-grained kind produced a unit series (%+v); it is already reported by "+
			"the workspace pair, and two series for one number double-count", unit)
	}
	if pass, ok := sweepFor(snap, "capture_digest_workspace"); !ok || pass.Failed != 1 {
		t.Errorf("the workspace-grained kind must still be reported by the workspace pair; got %+v (present=%t)", pass, ok)
	}
}

// TestARealRiverInsertCarriesTheSweepTagIntoTheColumnTheReadFilterson closes
// the gap every other test in this file leaves open: they seed the tag by
// hand, so they prove the query and not the pipeline. This one goes through
// River's own insert path with the tag on InsertOpts, and asserts the sweep
// read finds it — so a River release that stopped persisting tags, or an
// InsertOpts merge that dropped them, fails here rather than silently
// emptying the sweep gauges in production.
func TestARealRiverInsertCarriesTheSweepTagIntoTheColumnTheReadFiltersOn(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()

	// A runner of this test's own, because River refuses to insert a kind
	// its Workers bundle does not register and the shared helper registers
	// only its no-op.
	workers := river.NewWorkers()
	river.AddWorker(workers, &taggedWorker{})
	runner, err := jobs.New(pool, jobs.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	}, quietLogger())
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}

	if err := runner.Enqueue(ctx, taggedArgs{Workspace: ids.NewV7()},
		&river.InsertOpts{Tags: []string{jobs.SweepTag}}); err != nil {
		t.Fatalf("enqueueing a tagged job: %v", err)
	}

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	pass, ok := sweepFor(snap, taggedArgs{}.Kind())
	if !ok {
		t.Fatal("a job River inserted with the sweep tag is absent from the sweep read; " +
			"the tag did not survive the insert path the dispatchers use")
	}
	if pass.Workspaces != 1 {
		t.Errorf("Workspaces = %d, want 1", pass.Workspaces)
	}
}

// taggedArgs is a workspace-scoped kind for the round-trip above. It spells
// its workspace key the way every WorkspaceScoped kind does, because that
// spelling is what args->>'workspace_id' reads.
type taggedArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

func (taggedArgs) Kind() string { return "tagged_round_trip" }

// taggedWorker exists only so River accepts the insert; the round-trip
// asserts what the INSERT wrote, and never runs the job.
type taggedWorker struct {
	river.WorkerDefaults[taggedArgs]
}

func (taggedWorker) Work(context.Context, *river.Job[taggedArgs]) error { return nil }

// TestSweepIgnoresAWorkspacesSupersededFailure — the mirror of the above. A
// workspace that failed and then succeeded is not a tenant being missed.
func TestSweepIgnoresAWorkspacesSupersededFailure(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	ws := ids.NewV7()
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "discarded", Workspace: ws,
		Tags: []string{jobs.SweepTag}, CreatedAt: time.Now().Add(-2 * time.Hour),
	})
	seedJob(ctx, t, pool, seed{
		Kind: "sweep_child", State: "completed", Workspace: ws,
		Tags: []string{jobs.SweepTag}, CreatedAt: time.Now().Add(-time.Hour),
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	pass, ok := sweepFor(snap, "sweep_child")
	if !ok {
		t.Fatal("no sweep reported for a tagged kind")
	}
	if pass.Failed != 0 {
		t.Errorf("Failed = %d, want 0: the workspace recovered", pass.Failed)
	}
}

// TestSweepOmitsADispatchersOwnRow — a dispatcher carries no workspace, so
// it is not one workspace's share of anything. Counting it would add a
// phantom tenant to every sweep it tagged.
func TestSweepOmitsADispatchersOwnRow(t *testing.T) {
	_, pool := migratedAppPool(t)
	ctx := t.Context()
	seedJob(ctx, t, pool, seed{
		Kind: "the_dispatcher", State: "completed",
		Tags: []string{jobs.SweepTag},
	})

	snap, err := jobs.Stats(ctx, pool)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if _, ok := sweepFor(snap, "the_dispatcher"); ok {
		t.Error("an untenanted row was counted as a workspace's share of a fleet pass")
	}
}

// TestStatsOnAnEmptyJobTableIsAnEmptySnapshotNotAnError — the honest empty
// case. A fleet with nothing queued is a real state, and the reader above
// must be able to tell it from a failed read.
func TestStatsOnAnEmptyJobTableIsAnEmptySnapshotNotAnError(t *testing.T) {
	_, pool := migratedAppPool(t)

	snap, err := jobs.Stats(t.Context(), pool)
	if err != nil {
		t.Fatalf("Stats on an empty table: %v", err)
	}
	if len(snap.Rows) != 0 || len(snap.Sweeps) != 0 {
		t.Errorf("an empty job table produced %d rows and %d sweeps", len(snap.Rows), len(snap.Sweeps))
	}
}

// TestStatsFailsLoudlyWhenTheJobTableIsUnreachable — a read that could not
// happen must say so rather than answering with an empty snapshot, which
// downstream renders as a healthy idle fleet.
func TestStatsFailsLoudlyWhenTheJobTableIsUnreachable(t *testing.T) {
	_, pool := migratedAppPool(t)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := jobs.Stats(cancelled, pool); err == nil {
		t.Fatal("Stats answered a snapshot on a cancelled context; an unmeasured fleet " +
			"must not be indistinguishable from an empty one")
	}
}
