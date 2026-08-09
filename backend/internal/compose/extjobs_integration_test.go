// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The extension job seam, exercised end to end over a real migrated Postgres
// and a real River runner: a composed declaration becomes a cadenced
// dispatcher and a workspace child, the fan-out reaches every live tenant, the
// tick runs pinned to its own workspace under an authority re-derived at
// execution, and the two shapes this seam must never run are refused at boot.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// testJobDecl is the declaration every suite below composes from. Its cadence
// is deliberately long: RunOnStart is what fires the tick under test, and a
// short interval would let a second one land mid-assertion.
func testJobDecl() extension.JobDeclaration {
	return extension.JobDeclaration{
		Unit:              "jobtest",
		Job:               "refresh",
		Queue:             "default",
		Cadence:           time.Hour,
		DispatcherTimeout: 30 * time.Second,
		Timeout:           30 * time.Second,
		MaxAttempts:       2,
		Tier:              extension.TierAutoExecute,
		RequestedScope:    extension.ScopeRead,
	}
}

// composeJob registers one served extension job into this process's composed
// set and restores the empty set afterwards. The set is process-wide (it is a
// boot binding), so a test that left it behind would compose a phantom job into
// every suite that ran after it.
func composeJob(t *testing.T, decl extension.JobDeclaration, handle extension.JobHandler) {
	t.Helper()
	err := RegisterExtensions(
		[]extension.Extension{{
			Name:    decl.Unit,
			Version: "0.1.0",
			Jobs:    []extension.Job{{Name: decl.Job, Handle: handle}},
		}},
		nil,
		[]extension.JobDeclaration{decl},
	)
	if err != nil {
		t.Fatalf("RegisterExtensions: %v", err)
	}
	t.Cleanup(func() {
		setComposedJobs(nil)
		if err := jobs.RegisterComposed(nil); err != nil {
			t.Errorf("restoring the composed job table: %v", err)
		}
	})
}

// startRunner boots a runner carrying this process's composed set and returns
// its completed-job channel. Every core cadence is an hour, so the only ticks
// in flight are the RunOnStart ones.
func startRunner(t *testing.T, pool *pgxpool.Pool) (*jobs.Runner, <-chan *river.Event) {
	t.Helper()
	return startRunnerLogging(t, pool, slog.New(slog.DiscardHandler))
}

// startRunnerLogging is startRunner with the process logger under the test's
// control, for the one assertion that is ABOUT what was logged.
func startRunnerLogging(t *testing.T, pool *pgxpool.Pool, log *slog.Logger) (*jobs.Runner, <-chan *river.Event) {
	t.Helper()
	runner, err := NewJobRunner(pool, log, JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCompleted()
	t.Cleanup(cancelSub)
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
	return runner, sub
}

// seedWorkspaces adds n further live workspaces beside the fixture's, each with
// its own agent seat, and answers every live workspace id.
func seedWorkspaces(t *testing.T, e *integration.Env, n int) []ids.UUID {
	t.Helper()
	ctx := context.Background()
	// The OWNER connection, not e.Pool: the app pool is RLS-bound and the rows
	// below are the tenant boundary itself, which no tenant may write.
	owner := integration.OwnerConn(t)
	seedAgentSeat(t, e.WS)
	live := []ids.UUID{e.WS}
	for i := range n {
		ws := ids.NewV7()
		if _, err := owner.Exec(ctx,
			`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, $2, $3, 'EUR')`,
			ws, fmt.Sprintf("Fan %d", i), fmt.Sprintf("fan-%d", i)); err != nil {
			t.Fatalf("seeding workspace: %v", err)
		}
		seedAgentSeat(t, ws)
		live = append(live, ws)
	}
	return live
}

func seedAgentSeat(t *testing.T, ws ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := integration.OwnerConn(t).Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name, is_agent)
		 VALUES ($1, $2, $3, 'Agent', true)`, id, ws, id.String()+"@agent.test"); err != nil {
		t.Fatalf("seeding agent seat: %v", err)
	}
	return id
}

// awaitRows blocks until the job table holds want rows of kind, or the deadline
// fires. It reads the table rather than the subscription because what is under
// test is how many children the fan-out CREATED, and a subscription only
// reports the ones that also finished.
func awaitRows(t *testing.T, pool *pgxpool.Pool, kind string, want int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var got int
	for {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&got); err != nil {
			t.Fatalf("counting %s rows: %v", kind, err)
		}
		if got >= want {
			return got
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d rows of %s; saw %d", want, kind, got)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestDispatcherEnqueuesOneChildPerWorkspace: enablement is DIRECTORY PRESENCE
// and therefore global, so the fan-out is over ALL live workspaces — there is
// no per-tenant switch for the tick to consult, and a fan-out that reached only
// some of them would be enablement by accident.
func TestDispatcherEnqueuesOneChildPerWorkspace(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	live := seedWorkspaces(t, e, 2)

	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })
	startRunner(t, e.Pool)

	if got := awaitRows(t, e.Pool, decl.ChildKind(), len(live)); got != len(live) {
		t.Fatalf("child rows: got %d, want exactly one per live workspace (%d)", got, len(live))
	}
	// And each row is a DIFFERENT workspace: three rows that all named one
	// tenant would satisfy the count and none of the fan-out.
	var distinct int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT args ->> 'workspace_id') FROM river_job WHERE kind = $1`, decl.ChildKind()).Scan(&distinct); err != nil {
		t.Fatalf("counting distinct workspaces: %v", err)
	}
	if distinct != len(live) {
		t.Fatalf("distinct workspaces in the fan-out: got %d, want %d", distinct, len(live))
	}
}

// TestASeatlessWorkspaceIsSkippedAndCounted is the fresh-install case, and it
// is about what an OPERATOR sees rather than about a row count.
//
// Nothing in the product creates an agent seat (#656), so on a fresh
// installation every workspace is seatless — and a composed unit ships enabled
// at its declared cadence (crm-demo's is 60s). The dispatcher used to enqueue a
// child anyway, with a zero principal, so the tick failed at deriveAuthority;
// with MaxAttempts 3 that is three discarded rows a minute per workspace,
// forever, for a condition that is not a fault.
//
// So the assertion has three parts: the seatless tenant gets NO child, the
// seated tenants still get theirs (the skip is not a shutdown), and the
// condition is REPORTED as a gauge — because skipping without reporting would
// trade a log storm for a silently dead capability, which is the objection the
// original posture was written to answer.
func TestASeatlessWorkspaceIsSkippedAndCounted(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seated := seedWorkspaces(t, e, 1)

	// The fresh-install tenant: a live workspace with no agent seat at all.
	seatless := ids.NewV7()
	if _, err := integration.OwnerConn(t).Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'No Seat', 'no-seat', 'EUR')`,
		seatless); err != nil {
		t.Fatalf("seeding the seatless workspace: %v", err)
	}

	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })
	startRunner(t, e.Pool)

	// The seated tenants still fan out. awaitRows returns as soon as it sees at
	// least this many, so the "and no more" arm is checked separately below.
	if got := awaitRows(t, e.Pool, decl.ChildKind(), len(seated)); got != len(seated) {
		t.Fatalf("child rows: got %d, want exactly one per SEATED workspace (%d)", got, len(seated))
	}

	// The seatless tenant has no row of any state — not a failed one, not a
	// discarded one, not a retrying one. This is the assertion the finding is
	// about: an error stream is made of rows, and there are none.
	var rows int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args ->> 'workspace_id' = $2`,
		decl.ChildKind(), seatless.String()).Scan(&rows); err != nil {
		t.Fatalf("counting the seatless workspace's rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the seatless workspace has %d child row(s) — every one of them fails at the authority derivation, three times per cadence interval, forever", rows)
	}
	// And nothing anywhere failed: a skip that merely moved the failure to
	// another kind would satisfy the count above.
	var failed int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE state IN ('discarded', 'retryable') OR errors <> '{}'`).Scan(&failed); err != nil {
		t.Fatalf("counting failed rows: %v", err)
	}
	if failed != 0 {
		t.Fatalf("the fleet holds %d failed/retrying row(s); a fresh install must dispatch cleanly", failed)
	}

	// The condition is reported. Without this the skip would be silent, which
	// is the objection the enqueue-anyway posture was written to answer.
	if got := SeatlessWorkspaces(); got != 1 {
		t.Fatalf("the seatless gauge reads %d, want 1 — the skipped tenant is invisible to an operator", got)
	}
	var exposition bytes.Buffer
	if err := WriteSeatlessWorkspacesGauge(&exposition); err != nil {
		t.Fatalf("rendering the gauge: %v", err)
	}
	if !strings.Contains(exposition.String(), "margince_extension_job_seatless_workspaces 1") {
		t.Fatalf("the gauge is not in the exposition:\n%s", exposition.String())
	}
}

// TestChildrenUseByArgsUniqueness pins the trap the fan-out dies on.
//
// sweepInsertOpts — the shared periodic-pass policy — is ByState ONLY, so every
// child of one kind has the SAME unique key whatever workspace it names: N
// children collapse to one row, River reports no error, and the fan-out is
// silently a single-tenant pass. workspaceSweepOpts adds ByArgs, which puts the
// workspace in the key.
//
// Asserted twice on purpose. The first arm is the property (ByArgs is on), the
// second is the consequence (three tenants get three rows) — the property alone
// would still pass if the seam stopped calling this builder, and the
// consequence alone would not say which of the two builders produced it.
func TestChildrenUseByArgsUniqueness(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	live := seedWorkspaces(t, e, 2)

	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })

	opts := workspaceSweepOpts(decl.ChildKind())
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("the child's insert options do not deduplicate ByArgs — every workspace's child would share one unique key and the fan-out would collapse to a single row")
	}
	if len(opts.UniqueOpts.ByState) == 0 {
		t.Fatal("the child's insert options carry no in-flight state window — a tick would re-enqueue a workspace whose pass is still running")
	}
	if sweepInsertOpts().UniqueOpts.ByArgs {
		t.Fatal("sweepInsertOpts now deduplicates ByArgs — this test's whole premise is that it does not; re-read the fan-out's uniqueness argument")
	}

	startRunner(t, e.Pool)
	if got := awaitRows(t, e.Pool, decl.ChildKind(), len(live)); got != len(live) {
		t.Fatalf("child rows: got %d, want %d — a collapse to 1 is the ByState-only signature", got, len(live))
	}
}

// TestHandlerSeesAPinnedWorkspace: the runner binds the tenant from the child
// row's own args BEFORE the handler is entered, so a unit's tick can never see
// a global scope. It matters more for a job than for a served tool: a tool call
// arrives on a request whose tenant authentication already resolved, and a tick
// has no caller at all, so if the runner did not pin it nothing would.
func TestHandlerSeesAPinnedWorkspace(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	live := seedWorkspaces(t, e, 1)

	decl := testJobDecl()
	var mu sync.Mutex
	pinned := map[ids.UUID]bool{}
	unpinned := 0
	composeJob(t, decl, func(ctx context.Context, _ extension.Runtime) error {
		ws, ok := principal.WorkspaceID(ctx)
		mu.Lock()
		defer mu.Unlock()
		if !ok || ws == (ids.UUID{}) {
			unpinned++
			return nil
		}
		pinned[ws] = true
		return nil
	})
	_, sub := startRunner(t, e.Pool)
	awaitKindCompleted(mustDeadline(t), t, sub, decl.ChildKind())
	// Every tick of the fan-out, not just the first: one pinned handler and one
	// unpinned would still satisfy an any-of assertion.
	awaitRows(t, e.Pool, decl.ChildKind(), len(live))
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pinned) == len(live)
	}, "every tick to run under its own pinned workspace")
	mu.Lock()
	defer mu.Unlock()
	if unpinned != 0 {
		t.Fatalf("%d tick(s) ran with no workspace bound — a handler must never see a global scope", unpinned)
	}
	for _, ws := range live {
		if !pinned[ws] {
			t.Fatalf("workspace %s never saw a pinned tick", ws)
		}
	}
}

// TestPanickingTickFailsOneAttemptNotTheWorker: a unit's handler is ordinary
// third-party Go and may panic. What must survive is the runner — the blast
// radius of a bad tick is ONE attempt of ONE workspace's row, and every other
// kind on the same process keeps being worked.
func TestPanickingTickFailsOneAttemptNotTheWorker(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seedWorkspaces(t, e, 0)

	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error {
		panic("the unit's tick exploded")
	})
	logged := &syncBuffer{}
	runner, sub := startRunnerLogging(t, e.Pool, slog.New(slog.NewJSONHandler(logged, nil)))

	// The row records the failure: River never marks a panicking attempt
	// completed.
	waitUntil(t, func() bool {
		var errs int
		if err := e.Pool.QueryRow(context.Background(),
			`SELECT coalesce(cardinality(errors), 0) FROM river_job WHERE kind = $1 LIMIT 1`,
			decl.ChildKind()).Scan(&errs); err != nil {
			return false
		}
		return errs > 0
	}, "the panicking tick to be recorded as a failed attempt")

	// The stored failure is deliberately NOT the diagnosis: river_job.errors is
	// fleet-visible with no RLS, so jobs.FaultContext replaces an unclassified
	// cause with a fixed sentence, and a third-party unit's panic value is
	// exactly the text that must not land there.
	var stored string
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT errors[1] ->> 'error' FROM river_job WHERE kind = $1 LIMIT 1`,
		decl.ChildKind()).Scan(&stored); err != nil {
		t.Fatalf("reading the stored failure: %v", err)
	}
	if strings.Contains(stored, "exploded") {
		t.Fatalf("a unit's panic value reached the fleet-visible errors column:\n%s", stored)
	}

	// The ATTRIBUTION is in the log, and it is the only thing tick()'s own
	// recover buys over River's — which is what makes this the line that
	// separates testing this seam from testing River. River recovers a
	// panicking worker whatever we do, and `defer rt.release()` runs during
	// unwinding whether or not anything recovers, so everything asserted above
	// would hold against an implementation with no recover at all. What would
	// not hold is a log line naming WHOSE tick panicked: one args type serves
	// every composed job in the process, so River's own report names a shared
	// Go type and a kind string. Delete the recover in tick() and this goes red.
	waitUntil(t, func() bool {
		out := logged.String()
		return strings.Contains(out, `"unit":"jobtest"`) && strings.Contains(out, `"job":"refresh"`)
	}, "the panic to be logged against the unit and the job that raised it")

	// And the WORKER is still working. A close-date sweep enqueued after the
	// panic completes, which it could not if the panic had taken the runner
	// down with it.
	if err := runner.Enqueue(context.Background(), CloseDateSweepArgs{}, nil); err != nil {
		t.Fatalf("enqueueing after the panic: %v", err)
	}
	awaitKindCompleted(mustDeadline(t), t, sub, CloseDateSweepArgs{}.Kind())
}

// TestConfirmFirstJobIsRefusedAtBoot: a job has no caller, so a confirm-first
// tier is a confirmation nobody can ever give. adaptExtensionTool already
// refuses the served-tool equivalent because this surface cannot STAGE the
// approval; the job seam refuses a fortiori — there is no request to hold open
// and no one whose decision would be recorded, so the refusal does not wait on
// a staging seam that may one day be wired.
func TestConfirmFirstJobIsRefusedAtBoot(t *testing.T) {
	decl := testJobDecl()
	decl.Tier = extension.TierConfirmationRequired
	err := RegisterExtensions(
		[]extension.Extension{{Name: decl.Unit, Version: "0.1.0", Jobs: []extension.Job{{
			Name: decl.Job, Handle: func(context.Context, extension.Runtime) error { return nil },
		}}}},
		nil, []extension.JobDeclaration{decl})
	if err == nil {
		t.Fatal("the boot accepted a confirm-first job — a tick nobody can be asked about must not be registered")
	}
	// The composed set must be untouched by a refused registration: a boot that
	// aborts must leave nothing half-applied.
	if got := servedExtensionJobs(); len(got) != 0 {
		t.Fatalf("a refused boot left %d job(s) in the composed set", len(got))
	}
}

// TestEgressScopedJobIsRefusedAtBoot: send/enrich on a timer is autonomous
// outbound authority. A served TOOL spending an outbound cap is refused because
// leaving the workspace requires the confirm-first tier this surface cannot
// stage; a JOB cannot hold that tier at all (see above), so the only thing left
// to grant is outbound authority with no human anywhere in the loop.
func TestEgressScopedJobIsRefusedAtBoot(t *testing.T) {
	for _, scope := range []extension.Scope{extension.ScopeSend, extension.ScopeEnrich} {
		t.Run(string(scope), func(t *testing.T) {
			decl := testJobDecl()
			decl.RequestedScope = scope
			err := RegisterExtensions(
				[]extension.Extension{{Name: decl.Unit, Version: "0.1.0", Jobs: []extension.Job{{
					Name: decl.Job, Handle: func(context.Context, extension.Runtime) error { return nil },
				}}}},
				nil, []extension.JobDeclaration{decl})
			if err == nil {
				t.Fatalf("the boot accepted a %s-scoped job — a scheduled pass may not leave the workspace", scope)
			}
			if got := servedExtensionJobs(); len(got) != 0 {
				t.Fatalf("a refused boot left %d job(s) in the composed set", len(got))
			}
		})
	}
	// The two scopes that do NOT egress still compose, so the refusal above is
	// the egress rule and not a rule against every job.
	decl := testJobDecl()
	decl.RequestedScope = extension.ScopeWrite
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })
}

// TestStalePrincipalFailsClosed: the child row persists the initiating
// principal as a REFERENCE, and the authority behind it is re-derived at
// execution. A row can sit in the queue across a deactivation — enqueued,
// backed off, or rescued after a crash — and a principal serialised at enqueue
// time would keep working right through the revocation that was meant to end
// it. So a stale reference FAILS; it does not run reduced, and it does not
// quietly succeed having done nothing.
func TestStalePrincipalFailsClosed(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	decl := testJobDecl()
	worker := &extJobWorkspaceWorker{
		pool: e.Pool, decl: decl, log: slog.New(slog.DiscardHandler),
		handle: func(context.Context, extension.Runtime) error {
			t.Error("the tick ran with an authority that no longer exists")
			return nil
		},
	}
	ctx := context.Background()

	// A live seat derives, so the failures below are about the principal's
	// STATE and not about the derivation refusing everything.
	live := seedAgentSeat(t, e.WS)
	if _, err := worker.deriveAuthority(principal.WithWorkspaceID(ctx, e.WS),
		extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: live}); err != nil {
		t.Fatalf("deriving authority for a live seat: %v", err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(t *testing.T, id ids.UUID)
		absent  bool
		explain string
	}{
		{name: "deactivated", mutate: func(t *testing.T, id ids.UUID) {
			execAsOwner(t, `UPDATE app_user SET status = 'deactivated' WHERE id = $1`, id)
		}, explain: "a deactivated seat"},
		{name: "suspended", mutate: func(t *testing.T, id ids.UUID) {
			execAsOwner(t, `UPDATE app_user SET status = 'suspended' WHERE id = $1`, id)
		}, explain: "a suspended seat"},
		{name: "archived", mutate: func(t *testing.T, id ids.UUID) {
			execAsOwner(t, `UPDATE app_user SET archived_at = now() WHERE id = $1`, id)
		}, explain: "an archived seat"},
		{name: "never existed", absent: true, explain: "a principal that is not in this workspace at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := ids.NewV7()
			if !tc.absent {
				id = seedAgentSeat(t, e.WS)
				tc.mutate(t, id)
			}
			args := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: id}
			err := worker.Work(ctx, &river.Job[extJobWorkspaceArgs]{Args: args})
			if err == nil {
				t.Fatalf("%s ran the tick — a stale principal must fail the attempt, not run with the authority it held when the job was enqueued", tc.explain)
			}
			if !errors.Is(err, errStaleJobPrincipal) {
				t.Fatalf("%s failed with %v, want the stale-principal refusal", tc.explain, err)
			}
		})
	}

	// The zero reference is the same failure and not a special case: a
	// dispatcher that could not name an actor for a workspace still enqueues
	// the child (the fan-out stays total), and the tick is what says so.
	err := worker.Work(ctx, &river.Job[extJobWorkspaceArgs]{
		Args: extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS},
	})
	if !errors.Is(err, errStaleJobPrincipal) {
		t.Fatalf("a child carrying no principal failed with %v, want the stale-principal refusal", err)
	}
}

// TestOverlappingTicksAreBounded asserts the two bounds rather than inheriting
// them from River's defaults, because both defaults are wrong here: an unset
// attempt cap is a 25-rung ladder on attempt⁴ backoff in place of the
// dispatcher's own tick, and an unset uniqueness window lets every tick stack a
// second pass on a tenant whose first one is still running.
func TestOverlappingTicksAreBounded(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seedWorkspaces(t, e, 0)
	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })

	// The declared attempt cap reaches the row, not River's ladder.
	spec, ok := jobs.SpecFor(decl.ChildKind())
	if !ok {
		t.Fatal("the composed child kind is not declared — the seam registered a kind SpecFor cannot answer for")
	}
	if spec.MaxAttempts != decl.MaxAttempts {
		t.Fatalf("child attempt cap: got %d, want the declared %d", spec.MaxAttempts, decl.MaxAttempts)
	}

	inserter, err := jobs.NewInserter(e.Pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	ctx := context.Background()

	// A second DISPATCHER tick while the first is in flight is suppressed: the
	// periodic entry's window is activeSweepStates, which is what reproduced the
	// old ticker's one-pass-at-a-time across replicas.
	for range 2 {
		if err := inserter.Enqueue(ctx, extJobDispatcherArgs{JobKind: decl.DispatcherKind()}, sweepInsertOpts()); err != nil {
			t.Fatalf("enqueueing the dispatcher: %v", err)
		}
	}
	if got := countJobRows(t, e.Pool, decl.DispatcherKind()); got != 1 {
		t.Fatalf("dispatcher rows after two ticks: got %d, want 1 — an in-flight pass must suppress the next", got)
	}

	// And a second CHILD for one workspace while its pass is in flight, which
	// is the same window plus ByArgs.
	child := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: ids.NewV7()}
	for range 2 {
		if err := inserter.Enqueue(ctx, child, workspaceSweepOpts(decl.ChildKind())); err != nil {
			t.Fatalf("enqueueing the child: %v", err)
		}
	}
	if got := countJobRows(t, e.Pool, decl.ChildKind()); got != 1 {
		t.Fatalf("child rows for one workspace after two enqueues: got %d, want 1", got)
	}
	// A DIFFERENT workspace is not suppressed — the window bounds overlap, it
	// does not bound the fan-out.
	other := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: ids.NewV7(), Principal: ids.NewV7()}
	if err := inserter.Enqueue(ctx, other, workspaceSweepOpts(decl.ChildKind())); err != nil {
		t.Fatalf("enqueueing a second workspace's child: %v", err)
	}
	if got := countJobRows(t, e.Pool, decl.ChildKind()); got != 2 {
		t.Fatalf("child rows across two workspaces: got %d, want 2", got)
	}
}

func countJobRows(t *testing.T, pool *pgxpool.Pool, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatalf("counting %s rows: %v", kind, err)
	}
	return n
}

// execAsOwner runs one statement past RLS. Every use below mutates an
// app_user's own status or archival, which is the revocation the tick is meant
// to notice, and which no tenant-scoped connection may write.
func execAsOwner(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := integration.OwnerConn(t).Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// waitUntil polls a condition to a deadline, the same select-on-ctx shape
// awaitRows above uses: the condition is either a row River writes from
// another goroutine or a mutex-guarded map a job handler mutates on its own
// goroutine, and neither has a channel that reports "now check again" — so
// the wait is bounded by a context deadline and re-armed with time.After
// inside a select, never by an unconditional, uninterruptible time.Sleep.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if cond() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func mustDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestTheChildUniquenessKeyIsTheWorkspaceAlone is the other half of the ByArgs
// story, and the half a naive reading gets wrong.
//
// ByArgs hashes the WHOLE encoded args unless some field carries
// `river:"unique"`, and River adds the kind to that hash itself. Untagged, the
// key would be (kind, workspace, principal_id) — so a workspace whose recorded
// agent seat CHANGES between ticks gets a second concurrent child while the
// first is still in flight, and the overlap bound TestOverlappingTicksAreBounded
// asserts is not a bound at all. The reseat is not hypothetical: it is the
// deactivate-and-replace case deriveAuthority exists for.
//
// Deliberately not folded into TestOverlappingTicksAreBounded: that test reuses
// one args value for both enqueues, which cannot see this whatever the tag says.
func TestTheChildUniquenessKeyIsTheWorkspaceAlone(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	seedWorkspaces(t, e, 0)
	decl := testJobDecl()
	composeJob(t, decl, func(context.Context, extension.Runtime) error { return nil })

	inserter, err := jobs.NewInserter(e.Pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	ctx := context.Background()
	opts := workspaceSweepOpts(decl.ChildKind())

	// The SAME workspace, a DIFFERENT principal — the reseat. One tick's child
	// is still in flight when the next tick reads a new agent seat.
	first := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: ids.NewV7()}
	reseated := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: ids.NewV7()}
	if first.Principal == reseated.Principal {
		t.Fatal("the two principals are equal, so this test would pass with no tag at all")
	}
	for _, args := range []extJobWorkspaceArgs{first, reseated} {
		if err := inserter.Enqueue(ctx, args, opts); err != nil {
			t.Fatalf("enqueueing %s: %v", args.Principal, err)
		}
	}
	if got := countJobRows(t, e.Pool, decl.ChildKind()); got != 1 {
		t.Fatalf("child rows for one workspace across a reseat: got %d, want 1 — the uniqueness key is reading "+
			"principal_id, so a changed agent seat starts a second concurrent pass for that tenant", got)
	}

	// And the tag narrows the key without collapsing the fan-out: a different
	// WORKSPACE still gets its own row.
	other := extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: ids.NewV7(), Principal: first.Principal}
	if err := inserter.Enqueue(ctx, other, opts); err != nil {
		t.Fatalf("enqueueing a second workspace's child: %v", err)
	}
	if got := countJobRows(t, e.Pool, decl.ChildKind()); got != 2 {
		t.Fatalf("child rows across two workspaces: got %d, want 2 — the tag narrowed the key too far", got)
	}
}

// TestAPrincipalFromAnotherTenantDoesNotResolve is the arm deriveAuthority's
// doc comment leans on and that every other stale-principal case would pass
// without: the read is workspace-pinned, so a principal id belonging to a
// DIFFERENT tenant is not found here at all.
//
// It matters because the alternative — comparing workspace_id in the query —
// is a rule someone has to remember to write, and this one is the tenant policy
// the whole store already rides. The seat is deliberately live and valid in its
// own workspace, so the refusal can only come from the pin.
func TestAPrincipalFromAnotherTenantDoesNotResolve(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	other := ids.NewV7()
	if _, err := integration.OwnerConn(t).Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Other', 'other', 'EUR')`, other); err != nil {
		t.Fatalf("seeding the other workspace: %v", err)
	}
	foreign := seedAgentSeat(t, other)

	decl := testJobDecl()
	worker := &extJobWorkspaceWorker{
		pool: e.Pool, decl: decl, log: slog.New(slog.DiscardHandler),
		handle: func(context.Context, extension.Runtime) error {
			t.Error("the tick ran under another tenant's principal")
			return nil
		},
	}
	// The seat IS live and IS derivable — in its own workspace. Proving that
	// first is what makes the refusal below about the pin rather than about the
	// row's state.
	if _, err := worker.deriveAuthority(principal.WithWorkspaceID(context.Background(), other),
		extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: other, Principal: foreign}); err != nil {
		t.Fatalf("the foreign seat does not derive in its OWN workspace: %v", err)
	}

	err := worker.Work(context.Background(), &river.Job[extJobWorkspaceArgs]{
		Args: extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: foreign},
	})
	if !errors.Is(err, errStaleJobPrincipal) {
		t.Fatalf("a principal from another tenant gave %v, want the stale-principal refusal", err)
	}
}

// TestAHumanSeatIsNotAcceptedAsAJobPrincipal: deriveAuthority mints a
// PrincipalAgent, so the row it reads has to still BE an agent seat. Only the
// dispatcher writes the field today, which is why this is not reachable in
// production — but the function's premise is that the enqueue-time record is
// not trusted, and "the only writer is careful" is a property of today's
// callers rather than of the row.
func TestAHumanSeatIsNotAcceptedAsAJobPrincipal(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	decl := testJobDecl()
	worker := &extJobWorkspaceWorker{
		pool: e.Pool, decl: decl, log: slog.New(slog.DiscardHandler),
		handle: func(context.Context, extension.Runtime) error {
			t.Error("the tick ran as an agent principal derived from a human seat")
			return nil
		},
	}
	// e.Rep1 is the fixture's ordinary human: live, active, and not an agent.
	err := worker.Work(context.Background(), &river.Job[extJobWorkspaceArgs]{
		Args: extJobWorkspaceArgs{JobKind: decl.ChildKind(), Workspace: e.WS, Principal: e.Rep1},
	})
	if !errors.Is(err, errStaleJobPrincipal) {
		t.Fatalf("a human seat gave %v, want the stale-principal refusal", err)
	}
}

// syncBuffer is a writer several River goroutines log into at once. slog gives
// a handler no synchronisation of its own beyond the one write call, and
// bytes.Buffer is not safe for concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
