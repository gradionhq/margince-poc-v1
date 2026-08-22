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
