// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The router's OPENING announcement, end to end against a real database.
//
// aiactivity_router_integration_test.go proves the settling half: what a call
// turned out to be, once it was over. These prove the half that makes the rail
// worth watching — that the occurrence exists, and says it is working, while
// the model is still being asked.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
)

// start announces one call's beginning the way the router does before it serves
// an attempt. The lease is the caller's, so a test can pin what the projection
// derives stale_after from rather than restating the router's own arithmetic.
func (f *routerFixture) start(t *testing.T, task ai.Task, lease time.Duration) {
	t.Helper()
	f.meter.AnnounceRailStart(f.ctx, ai.Call{
		LogicalCallID: f.corr,
		Task:          task,
		CorrelationID: &f.corr,
	}, lease)
}

// The whole point of the change: a rep who asks for a summary sees the work
// while it is happening, not a line that appears already finished.
//
// Asserted as a TRANSITION rather than as two independent states. A test that
// only checked the settled row would pass against a router that never announced
// a start at all — which is exactly the behaviour this replaces.
func TestACallSaysItIsRunningBeforeItSaysWhatItDid(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskSummarize, 5*time.Minute)
	f.drain(t)

	live := f.row(t, ai.TaskSummarize)
	if live.State != "running" {
		t.Fatalf("state after the start announcement = %q, want running", live.State)
	}
	if live.StartedAt == nil {
		t.Error("a running occurrence carries no started_at, which ai_task_run_queued_has_no_start forbids for any non-queued state")
	}
	if live.FinishedAt != nil {
		t.Errorf("a running occurrence carries finished_at %s, so the settled feed would order by a finish that has not happened", live.FinishedAt)
	}
	if live.StaleAfter == nil {
		t.Fatal("a running occurrence carries no lease, so a process killed here would claim to be working forever")
	}
	if !live.StaleAfter.After(*live.StartedAt) {
		t.Errorf("lease expires at %s, at or before the start %s — the occurrence is stale the instant it appears", live.StaleAfter, live.StartedAt)
	}

	f.call(t, ai.TaskSummarize, nil)
	f.drain(t)

	settled := f.row(t, ai.TaskSummarize)
	if settled.State != "done" {
		t.Errorf("state after the call settled = %q, want done", settled.State)
	}
	if settled.FinishedAt == nil {
		t.Error("a settled occurrence carries no finished_at")
	}
	if settled.Attempt != live.Attempt {
		t.Errorf("the start announced attempt %d and the settle announced %d — a settle that outranks its own start reopens the occurrence instead of closing it",
			live.Attempt, settled.Attempt)
	}
}

// One line, not two. The start and the settle are the same occurrence, and a
// key that disagreed between them would put a permanently-running row beside
// the finished one — the rail claiming a piece of work is both.
func TestTheStartAndTheSettleAreOneOccurrence(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskSummarize, 5*time.Minute)
	f.call(t, ai.TaskSummarize, nil)
	f.drain(t)

	var rows int
	if err := f.env.Pool.QueryRow(t.Context(), `
		SELECT count(*) FROM ai_task_run WHERE source = $1 AND ai_task = $2`,
		"ai_router", string(ai.TaskSummarize)).Scan(&rows); err != nil {
		t.Fatalf("counting the occurrences: %v", err)
	}
	if rows != 1 {
		t.Fatalf("a start and a settle produced %d occurrences, want 1", rows)
	}
}

// The settled row must LOSE its lease. stale_after is what makes a live row
// render stalled, and a settled row that kept one is a finished piece of work
// carrying a deadline it can no longer miss — harmless today only because the
// read derives stalled for live states, which is exactly the kind of guarantee
// that stops being true when somebody widens the arm.
func TestASettledOccurrenceKeepsNoLease(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskSummarize, 5*time.Minute)
	f.call(t, ai.TaskSummarize, nil)
	f.drain(t)

	if got := f.row(t, ai.TaskSummarize); got.StaleAfter != nil {
		t.Errorf("a settled occurrence still leases until %s", got.StaleAfter)
	}
}

// The lease the router derived is the lease the projection stores.
//
// Two readings of one value, and nothing else checks they agree: railLease
// computes a duration, the handler turns it into an instant, and a unit test of
// either half passes while the two disagree. A distinctive value rather than
// the five minutes the other tests use, so a projection that ignored the
// emitter's lease and substituted a default of its own would be caught rather
// than accidentally matched.
func TestTheProjectionStoresTheLeaseTheRouterDerived(t *testing.T) {
	f := newRouterFixture(t)
	const lease = 97 * time.Second

	f.start(t, ai.TaskSummarize, lease)
	f.drain(t)

	got := f.row(t, ai.TaskSummarize)
	if got.StartedAt == nil || got.StaleAfter == nil {
		t.Fatalf("a running occurrence is missing started_at (%v) or stale_after (%v)", got.StartedAt, got.StaleAfter)
	}
	if held := got.StaleAfter.Sub(*got.StartedAt); held != lease {
		t.Errorf("the projection leased the occurrence for %s, and the router asked for %s", held, lease)
	}
}

// A call outside a correlation scope announces no start, for the same reason it
// announces no settle: storekit.Emit refuses an envelope without a correlation
// id, so the occurrence cannot exist. Opening one anyway would be worse than
// silence — the flush that would close it is refused too, so the row would
// claim to be working until its lease ran out.
func TestAStartOutsideACorrelationScopeIsNotAnnounced(t *testing.T) {
	f := newRouterFixture(t)

	f.meter.AnnounceRailStart(f.ctx, ai.Call{
		LogicalCallID: f.corr,
		Task:          ai.TaskSummarize,
	}, 5*time.Minute)
	f.drain(t)

	var rows int
	if err := f.env.Pool.QueryRow(t.Context(), `
		SELECT count(*) FROM ai_task_run WHERE source = $1`, "ai_router").Scan(&rows); err != nil {
		t.Fatalf("counting the occurrences: %v", err)
	}
	if rows != 0 {
		t.Fatalf("a call with no correlation id opened %d occurrence(s) that no flush can ever close", rows)
	}
}

// A sub-second lease must still be a lease. The projection reads stale_after as
// the whole answer to "is this row still believable", so a lease that truncated
// to zero seconds would arrive as NO lease — and a running occurrence without
// one is the row that claims to be working forever, which is the single failure
// the lease exists to prevent.
func TestASubSecondLeaseIsStillALease(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskSummarize, 500*time.Millisecond)
	f.drain(t)

	got := f.row(t, ai.TaskSummarize)
	if got.StaleAfter == nil {
		t.Fatal("a sub-second lease produced no stale_after, so the occurrence can never read stalled")
	}
	if got.StartedAt == nil || !got.StaleAfter.After(*got.StartedAt) {
		t.Errorf("lease expires at %v, at or before the start %v", got.StaleAfter, got.StartedAt)
	}
}

// A carrier owns the whole occurrence, both ends of it. The router staying
// silent at the settle is already gated; this is the OTHER direction, and it is
// the one a new emitter gets wrong — announcing a start for work whose carrier
// will announce its own puts two writers on one line.
func TestTheRouterAnnouncesNoStartForACarrierOwnedTask(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskAgentLoop, 5*time.Minute)
	f.drain(t)

	var rows int
	if err := f.env.Pool.QueryRow(t.Context(), `
		SELECT count(*) FROM ai_task_run WHERE source = $1`, "ai_router").Scan(&rows); err != nil {
		t.Fatalf("counting the occurrences: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the router announced %d start(s) for a task the agent runner reports", rows)
	}
}

// dbNow reads the DATABASE's clock, which is the one that matters here.
//
// stale_after is stamped from the database (started_at + the lease), and the
// sweep compares it against a cutoff the caller supplies. A cutoff taken from
// the test host compares two clocks, so these assertions would turn on host
// drift rather than on the predicate under test — and would fail on somebody
// else's machine for a reason that has nothing to do with the sweep.
func (f *routerFixture) dbNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	if err := f.env.Pool.QueryRow(t.Context(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("reading the database clock: %v", err)
	}
	return now
}

// sweep closes the router occurrences whose lease ran out before cutoff, the
// way the retention job does.
func (f *routerFixture) sweep(t *testing.T, cutoff time.Time) int64 {
	t.Helper()
	closed, err := aiactivity.NewStore(f.env.DB()).CloseAbandonedRouterRuns(f.ctx, cutoff)
	if err != nil {
		t.Fatalf("closing abandoned router occurrences: %v", err)
	}
	return closed
}

// A start that nothing ever settles must not sit on a rep's rail forever.
//
// This is the failure the start announcement CREATES and the reason the sweep
// exists: the router commits its start in its own transaction before the call,
// and the only thing that closes it is a flush that is best-effort by design. A
// flush that times out, or a process killed mid-call, leaves a row that renders
// stalled once its lease expires — and nothing else would ever reach it, because
// the live feed has no time bound and the settled purge only sees settled rows.
func TestAStartNothingSettledIsClosedByTheSweep(t *testing.T) {
	f := newRouterFixture(t)

	// A one-second lease and a cutoff past it, rather than a backdated row: the
	// sweep takes its cutoff from the caller precisely so a test can reach the
	// real predicate without writing state the real writer never writes.
	f.start(t, ai.TaskSummarize, time.Second)
	f.drain(t)
	if got := f.row(t, ai.TaskSummarize); got.State != "running" {
		t.Fatalf("state before the sweep = %q, want running", got.State)
	}

	if closed := f.sweep(t, f.dbNow(t).Add(time.Hour)); closed != 1 {
		t.Fatalf("the sweep closed %d occurrences, want 1", closed)
	}

	got := f.row(t, ai.TaskSummarize)
	if got.State != "failed" {
		t.Errorf("state after the sweep = %q, want failed", got.State)
	}
	if got.FinishedAt == nil {
		t.Error("the sweep settled the row without a finished_at, which ai_task_run_settled_has_finish forbids")
	}
	if got.StaleAfter != nil {
		t.Errorf("a settled row still leases until %s", got.StaleAfter)
	}
	if got.DegradeReason == nil || *got.DegradeReason != "abandoned" {
		t.Errorf("degrade reason = %v, want the sweep's own closed reason", got.DegradeReason)
	}
}

// The sweep must not close a call that is simply still working. Its whole
// predicate is the lease, and a sweep that ignored it would settle live work
// under a rep's eyes — the opposite failure, and the more visible one.
func TestTheSweepLeavesAnOccurrenceInsideItsLease(t *testing.T) {
	f := newRouterFixture(t)

	f.start(t, ai.TaskSummarize, time.Hour)
	f.drain(t)

	if closed := f.sweep(t, f.dbNow(t)); closed != 0 {
		t.Fatalf("the sweep closed %d occurrences that are still inside their lease, want 0", closed)
	}
	if got := f.row(t, ai.TaskSummarize); got.State != "running" {
		t.Errorf("state after the sweep = %q, want running — the sweep settled work that is still happening", got.State)
	}
}

// A CARRIER's live occurrence is not the sweep's to close, however old.
//
// The distinction is the reason the predicate names ai_router rather than a
// state: a carrier holds a durable row it can re-arm from, so a live line of its
// is a claim it still owns. The router holds no such claim, which is the whole
// argument for sweeping its rows and only its rows.
func TestTheSweepDoesNotCloseACarriersLiveOccurrence(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)
	if err := f.runs.EnqueueJob(ctx, f.spec.Name, f.trigger, &f.passport, f.dbNow(t)); err != nil {
		t.Fatalf("enqueuing the run: %v", err)
	}
	if _, created, err := f.runs.StartRun(ctx, f.spec, f.trigger, f.passport); err != nil || !created {
		t.Fatalf("claiming the run: created=%v err=%v", created, err)
	}
	f.drain(t)

	live, _ := f.feed(t)
	if len(live) != 1 {
		t.Fatalf("the runner has %d live occurrences before the sweep, want 1", len(live))
	}

	closed, err := aiactivity.NewStore(f.env.DB()).CloseAbandonedRouterRuns(ctx, f.dbNow(t).Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("closing abandoned router occurrences: %v", err)
	}
	if closed != 0 {
		t.Fatalf("the sweep closed %d carrier-owned occurrences, want 0", closed)
	}
	if live, _ = f.feed(t); len(live) != 1 {
		t.Errorf("the runner has %d live occurrences after the sweep, want 1", len(live))
	}
}
