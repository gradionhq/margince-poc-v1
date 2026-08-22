// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The router reports the tasks no carrier owns, end to end.
//
// Every occurrence here is BORN through ai.CallMeter.Record — the one writer
// the router's flush calls — and every envelope is READ BACK OUT of
// event_outbox rather than hand-built, so what the consumer receives is exactly
// what production staged. A hand-inserted ai_task_run row would prove nothing
// about either half.
//
// What this covers that the carrier suites cannot: the DEFAULT reporter. A
// carrier is wired one task at a time and its suite proves that one task; the
// router is what makes the other nineteen report at all, and it is the half
// that has to keep working for a task nobody has thought about yet.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// routerFixture is the real meter, the real consumer, and one correlation id
// standing for one request or job pass.
type routerFixture struct {
	env       *Env
	ctx       context.Context
	meter     *ai.CallMeter
	consumer  *aiactivity.Consumer
	corr      ids.UUID
	delivered int
}

func newRouterFixture(t *testing.T) *routerFixture {
	t.Helper()
	e := Setup(t)
	return &routerFixture{
		env: e,
		// The admin's own context, so the occurrence is attributable the way a
		// real request's is. A bare workspace context would exercise the
		// unattributed path and quietly prove nothing about who the work
		// belongs to.
		ctx: e.Admin(),
		// The consumer logs INTO THE TEST. A deterministic refusal is acked
		// away by design, so a discarding logger turns a refusal into a row
		// that never appeared — a failure two steps from its cause.
		consumer: aiactivity.NewConsumer(aiactivity.NewStore(e.DB()), testLogger(t)),
		meter:    ai.NewCallMeter(e.DB()).WithLogger(testLogger(t)),
		corr:     ids.NewV7(),
	}
}

// call records one finished model call the way the router's flush does.
func (f *routerFixture) call(t *testing.T, task ai.Task, mutate func(*ai.Call)) {
	t.Helper()
	c := ai.Call{
		LogicalCallID:        ids.NewV7(),
		Attempt:              1,
		IsTerminal:           true,
		Kind:                 "completion",
		CorrelationID:        &f.corr,
		Task:                 task,
		Tier:                 ai.TierCheapCloud,
		Provider:             "anthropic",
		ModelID:              "claude-cheap",
		ServedIdentitySource: "response",
		RequestFingerprint:   "fp",
		LatencyMS:            1200,
	}
	if mutate != nil {
		mutate(&c)
	}
	if err := f.meter.Record(f.ctx, []ai.Call{c}); err != nil {
		t.Fatalf("recording a %s call: %v", task, err)
	}
}

// drain hands the consumer every ai_task.state_changed the router has staged,
// oldest first — what a subscriber that is keeping up receives.
func (f *routerFixture) drain(t *testing.T) {
	t.Helper()
	var raws [][]byte
	err := f.env.DB().Tx(context.Background(), func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT envelope FROM event_outbox
			 WHERE envelope->>'type' = 'ai_task.state_changed'
			   AND envelope->'payload'->>'source' = $1
			 ORDER BY seq
			 OFFSET $2`, "ai_router", f.delivered)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			raws = append(raws, raw)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("reading the staged envelopes: %v", err)
	}
	for _, raw := range raws {
		var env kevents.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decoding a staged envelope: %v", err)
		}
		if err := f.consumer.HandleEvent(context.Background(), env); err != nil {
			t.Fatalf("the projection refused envelope %s: %v", env.EventID, err)
		}
		f.delivered++
	}
}

// routerRow is the occurrence as the projection holds it.
type routerRow struct {
	Kind          string
	AITask        *string
	State         string
	Attempt       int
	ActorScope    string
	ActorUserID   *ids.UUID
	StartedAt     *time.Time
	FinishedAt    *time.Time
	StaleAfter    *time.Time
	DegradeReason *string
}

func (f *routerFixture) row(t *testing.T, task ai.Task) routerRow {
	t.Helper()
	var got routerRow
	err := f.env.Pool.QueryRow(context.Background(), `
		SELECT kind, ai_task, state, attempt, actor_scope, actor_user_id,
		       started_at, finished_at, stale_after, degrade_reason
		  FROM ai_task_run WHERE source = $1 AND occurrence_key = $2`,
		"ai_router", f.corr.String()+":"+string(task)).
		Scan(&got.Kind, &got.AITask, &got.State, &got.Attempt, &got.ActorScope,
			&got.ActorUserID, &got.StartedAt, &got.FinishedAt, &got.StaleAfter,
			&got.DegradeReason)
	if err != nil {
		t.Fatalf("reading the projected occurrence for %s: %v", task, err)
	}
	return got
}

// A task no carrier owns still reaches the rail, attributed to the person whose
// request made the call, settled the moment it appears — and carrying no lease,
// because it never claimed to be running.
func TestARouterReportedTaskReachesTheProjection(t *testing.T) {
	f := newRouterFixture(t)
	f.call(t, ai.TaskSummarize, nil)
	f.drain(t)

	got := f.row(t, ai.TaskSummarize)
	if got.Kind != string(ai.TaskSummarize) {
		t.Errorf("kind = %q, want the task's own name", got.Kind)
	}
	if got.AITask == nil || *got.AITask != string(ai.TaskSummarize) {
		t.Errorf("ai_task = %v, want %q — without it the row joins to no cost and no certification record", got.AITask, ai.TaskSummarize)
	}
	if got.State != "done" {
		t.Errorf("state = %q, want done", got.State)
	}
	if got.ActorScope != "personal" || got.ActorUserID == nil || *got.ActorUserID != f.env.AdminUser {
		t.Errorf("actor = (%s, %v), want the admin who asked for the work", got.ActorScope, got.ActorUserID)
	}
	if got.StaleAfter != nil {
		t.Errorf("stale_after = %v, want none — a settled occurrence has no live attempt whose believability could expire", got.StaleAfter)
	}
	if got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("started_at = %v, finished_at = %v, want both — a settled row without a finish breaks the feed's own CHECK", got.StartedAt, got.FinishedAt)
	}
	// The start is derived by winding the call's own latency back off the
	// database's clock, so the pair has to bracket the call rather than collapse.
	if ran := got.FinishedAt.Sub(*got.StartedAt); ran != 1200*time.Millisecond {
		t.Errorf("the occurrence ran for %s, want the call's own 1.2s latency", ran)
	}
}

// One request's calls for one task are ONE line. Forty page reads under a
// single job pass must not become forty rows on somebody's rail, and the second
// call must WIN rather than be refused as a redelivery of the first — otherwise
// a failure a retry has already corrected is reported forever.
func TestOneRequestsCallsForOneTaskAreOneOccurrence(t *testing.T) {
	f := newRouterFixture(t)
	f.call(t, ai.TaskSiteFactExtract, func(c *ai.Call) { c.ErrorSentinel = "provider_unavailable" })
	f.drain(t)
	if got := f.row(t, ai.TaskSiteFactExtract); got.State != "failed" || got.Attempt != 1 {
		t.Fatalf("first call projected (%s, attempt %d), want (failed, attempt 1)", got.State, got.Attempt)
	}

	f.call(t, ai.TaskSiteFactExtract, nil)
	f.drain(t)

	if n := f.env.WsCount(t, `SELECT count(*) FROM ai_task_run WHERE source = 'ai_router'`); n != 1 {
		t.Errorf("ai_router occurrences = %d, want 1 — the two calls are one piece of work", n)
	}
	got := f.row(t, ai.TaskSiteFactExtract)
	if got.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 — a second call under one key that reused attempt 1 would be refused by the projection's guard", got.Attempt)
	}
	if got.State != "done" {
		t.Errorf("state = %q, want done — the retry corrected the failure and the rail must stop reporting it", got.State)
	}
	if got.DegradeReason != nil {
		t.Errorf("degrade_reason = %q survived the correction", *got.DegradeReason)
	}
}

// Two tasks under one request are two occurrences: they are different work, and
// collapsing them would let one overwrite the other's outcome.
func TestTwoTasksUnderOneRequestAreTwoOccurrences(t *testing.T) {
	f := newRouterFixture(t)
	f.call(t, ai.TaskSummarize, nil)
	f.call(t, ai.TaskGrowthFit, func(c *ai.Call) { c.Degraded = true })
	f.drain(t)

	if got := f.row(t, ai.TaskSummarize); got.State != "done" {
		t.Errorf("summarize = %q, want done", got.State)
	}
	if got := f.row(t, ai.TaskGrowthFit); got.State != "degraded" {
		t.Errorf("growth_fit = %q, want degraded — partial state was kept and MUST NOT read as done", got.State)
	}
}

// The registry silences the router where a carrier owns the task. Both would
// otherwise write one occurrence between them, and the carrier's is the one
// that can say queued and running.
func TestTheRouterWritesNothingForACarrierOwnedTask(t *testing.T) {
	f := newRouterFixture(t)
	f.call(t, ai.TaskDocumentExtract, nil)

	if n := f.env.WsCount(t, `SELECT count(*) FROM ai_call`); n != 1 {
		t.Fatalf("ai_call rows = %d, want 1 — the call must still be TRACED, only unannounced", n)
	}
	if n := f.env.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'ai_task.state_changed'
		    AND envelope->'payload'->>'source' = 'ai_router'`); n != 0 {
		t.Errorf("the router staged %d announcements for a task attachment extraction owns", n)
	}
}

// An errored call reaches the rail carrying the CLOSED sentinel, never a
// provider's own message: degrade_reason is read by an ordinary rep, and vendor
// error text carries provider detail and can echo credential material.
func TestAFailedCallCarriesItsSentinelAndNoVendorText(t *testing.T) {
	f := newRouterFixture(t)
	f.call(t, ai.TaskEnrich, func(c *ai.Call) { c.ErrorSentinel = "budget_exceeded" })
	f.drain(t)

	got := f.row(t, ai.TaskEnrich)
	if got.State != "failed" {
		t.Errorf("state = %q, want failed", got.State)
	}
	if got.DegradeReason == nil || *got.DegradeReason != "budget_exceeded" {
		t.Errorf("degrade_reason = %v, want the closed sentinel", got.DegradeReason)
	}
}

// A call the meter cannot announce is still TRACED. The trace is what the
// budget guardrail, the cost ledger and the certification record are read from,
// and none of them may be lost because a rail row could not be written.
func TestACallTheRouterCannotAnnounceIsStillTraced(t *testing.T) {
	e := Setup(t)
	// A workspace-bound context with no actor: storekit has no principal to
	// stamp the ledger row from, so the announcement cannot be made at all.
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	meter := ai.NewCallMeter(e.DB()).WithLogger(testLogger(t))
	corr := ids.NewV7()
	if err := meter.Record(ctx, []ai.Call{{
		LogicalCallID: ids.NewV7(), Attempt: 1, IsTerminal: true, Kind: "completion",
		CorrelationID: &corr, Task: ai.TaskSummarize, Tier: ai.TierCheapCloud,
		Provider: "anthropic", ModelID: "claude-cheap", ServedIdentitySource: "response",
		RequestFingerprint: "fp",
	}}); err != nil {
		t.Fatalf("an unannounceable call failed its trace: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call`); n != 1 {
		t.Errorf("ai_call rows = %d, want 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_task_run WHERE source = 'ai_router'`); n != 0 {
		t.Errorf("ai_task_run rows = %d, want 0 — an occurrence nobody can be attributed for must not be invented", n)
	}
}

// The filter is what makes "every task reports, the client decides" work at all.
//
// `recent` is bounded at ten and the server now reports every AI task, so a
// client that draws three kinds and asks for none can be handed ten rows it
// renders nothing for — the rail goes blank on the day somebody used the
// composer a lot, while the projection was right the whole time. The bound has
// to fall INSIDE the caller's own set, which is a property of where the
// predicate sits in the statement and cannot be got by filtering the result.
func TestTheBoundFallsInsideTheKindsTheCallerAskedFor(t *testing.T) {
	f := newRouterFixture(t)
	// Twelve settled occurrences of a kind the caller does not want, more than
	// the ten `recent` holds, and then the one it does.
	for range 12 {
		f.corr = ids.NewV7()
		f.call(t, ai.TaskDraftReply, nil)
	}
	f.corr = ids.NewV7()
	f.call(t, ai.TaskSummarize, nil)
	f.drain(t)

	asked := []string{string(ai.TaskSummarize)}
	_, settled, err := aiactivity.NewStore(f.env.DB()).Mine(f.ctx, f.midnightOf(t), asked)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	if len(settled) != 1 {
		t.Fatalf("settled = %d rows, want the 1 kind the caller asked for — the bound fell on the whole record", len(settled))
	}
	if settled[0].Kind != string(ai.TaskSummarize) {
		t.Errorf("settled kind = %q, want %q", settled[0].Kind, ai.TaskSummarize)
	}

	// And the unfiltered read is still the COMPLETE record: an omitted filter
	// must not quietly inherit whatever the last caller asked for.
	_, everything, err := aiactivity.NewStore(f.env.DB()).Mine(f.ctx, f.midnightOf(t), nil)
	if err != nil {
		t.Fatalf("Mine unfiltered: %v", err)
	}
	if len(everything) != 10 {
		t.Errorf("the unfiltered feed returned %d rows, want the bound's 10 — the complete record is what an unfiltered read is for", len(everything))
	}
}

// midnightOf is the start of the database's today, read from the database: the
// rows were stamped against that clock and the boundary has to be the same one.
func (f *routerFixture) midnightOf(t *testing.T) time.Time {
	t.Helper()
	var midnight time.Time
	if err := f.env.Pool.QueryRow(context.Background(),
		`SELECT date_trunc('day', now())`).Scan(&midnight); err != nil {
		t.Fatalf("reading the database's idea of today: %v", err)
	}
	return midnight
}

// Concurrent calls of one task under one correlation id ALL survive.
//
// railAttempt COUNTS, and at READ COMMITTED a transaction cannot see another's
// uncommitted ai_call row — so two that overlap count the same value, announce
// the same attempt, and the projection's guard refuses the second as a
// redelivery of the first. The LATER outcome is the one lost, so a failure can
// outlive the retry that already fixed it, with nothing logged anywhere.
//
// site_fact_extract is the subject on purpose: the deep read's fact lane is
// page-PARALLEL by design, so one read fires many of these at once under one
// correlation id. This is the shape production already has.
//
// It takes a CROWD, and that is the honest part. A two-goroutine version of this
// test passed with the lock removed, three runs out of three — two transactions
// racing from a channel close rarely overlap inside the count, so it vouched for
// a guard it never exercised. The width here is what makes the window real:
// every worker's attempt must be distinct, and a single collision among them is
// one lost outcome.
const racingCalls = 16

func TestConcurrentCallsOfOneTaskAllReachTheProjection(t *testing.T) {
	f := newRouterFixture(t)

	start := make(chan struct{})
	errs := make(chan error, racingCalls)
	for i := range racingCalls {
		// A meter EACH: one meter reused would still take its own connection
		// per call, but a separate one keeps the test honest about there being
		// no shared Go-side state doing the serializing.
		meter := ai.NewCallMeter(f.env.DB()).WithLogger(testLogger(t))
		go func() {
			<-start
			errs <- meter.Record(f.ctx, []ai.Call{{
				LogicalCallID: ids.NewV7(), Attempt: 1, IsTerminal: true, Kind: "completion",
				CorrelationID: &f.corr, Task: ai.TaskSiteFactExtract, Tier: ai.TierCheapCloud,
				Provider: "anthropic", ModelID: "claude-cheap", ServedIdentitySource: "response",
				// The last one to land wins the row, so every worker carries a
				// distinguishable outcome rather than all of them agreeing.
				RequestFingerprint: "fp", ErrorSentinel: racingSentinel(i),
			}})
		}()
	}
	close(start)
	for range racingCalls {
		if err := <-errs; err != nil {
			t.Fatalf("a racing record failed: %v", err)
		}
	}

	// Every announcement reached the bus — that half never depended on the lock.
	staged := f.env.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'ai_task.state_changed'
		    AND envelope->'payload'->>'source' = 'ai_router'`)
	if staged != racingCalls {
		t.Fatalf("the router staged %d announcements for %d calls, want all of them", staged, racingCalls)
	}

	// The attempts must be DISTINCT. This is the assertion the lock is for: two
	// that agree are two announcements the projection can only keep one of.
	distinct := f.env.WsCount(t,
		`SELECT count(DISTINCT (envelope->'payload'->>'attempt')) FROM event_outbox
		  WHERE envelope->>'type' = 'ai_task.state_changed'
		    AND envelope->'payload'->>'source' = 'ai_router'`)
	if distinct != racingCalls {
		t.Errorf("%d of %d concurrent calls announced a distinct attempt — the rest collided, "+
			"and the projection keeps only one outcome per (attempt, rank)", distinct, racingCalls)
	}

	// And the projection agrees: one occurrence, at the highest attempt.
	f.drain(t)
	if n := f.env.WsCount(t, `SELECT count(*) FROM ai_task_run WHERE source = 'ai_router'`); n != 1 {
		t.Errorf("ai_router occurrences = %d, want 1 — they are one piece of work", n)
	}
	if got := f.row(t, ai.TaskSiteFactExtract); got.Attempt != racingCalls {
		t.Errorf("attempt = %d, want %d — an outcome was refused as a redelivery and lost", got.Attempt, racingCalls)
	}
}

// racingSentinel gives each worker its own terminal outcome, so a collision
// cannot hide behind two workers that happened to say the same thing.
func racingSentinel(i int) string {
	if i%2 == 0 {
		return ""
	}
	return "provider_unavailable"
}

// A call that cannot get the occurrence lock is still TRACED.
//
// This is the failure the savepoint exists for, and an earlier version of this
// code got it exactly wrong: the lock was taken on the OUTER transaction, so a
// lock error poisoned the trace transaction itself and every ai_call row of the
// logical call was lost at COMMIT — while the code above it claimed the
// announcement could not break the trace. The trace is what the budget
// guardrail, the cost ledger and the certification record are read from.
//
// The lock is held from a SEPARATE connection for longer than the bounded wait,
// which is the only way to make the timeout fire deterministically: a race
// between goroutines would prove nothing, since the loser usually gets the lock
// a moment later.
func TestACallThatCannotGetTheOccurrenceLockIsStillTraced(t *testing.T) {
	f := newRouterFixture(t)

	holder, err := f.env.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquiring the holding connection: %v", err)
	}
	defer holder.Release()
	held, err := holder.Begin(context.Background())
	if err != nil {
		t.Fatalf("opening the holding transaction: %v", err)
	}
	// The same key the announcement will build, taken the same way, so this is
	// the lock it really contends for and not one that merely looks like it.
	if err := lockRailOccurrence(t, held, f.env.WS, f.corr, ai.TaskSummarize); err != nil {
		t.Fatalf("taking the occurrence lock: %v", err)
	}

	f.call(t, ai.TaskSummarize, nil)

	if err := held.Rollback(context.Background()); err != nil {
		t.Fatalf("releasing the occurrence lock: %v", err)
	}

	if n := f.env.WsCount(t, `SELECT count(*) FROM ai_call`); n != 1 {
		t.Errorf("ai_call rows = %d, want 1 — a lock the announcement could not get took the whole trace with it", n)
	}
	if n := f.env.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'ai_task.state_changed'
		    AND envelope->'payload'->>'source' = 'ai_router'`); n != 0 {
		t.Errorf("the router staged %d announcements despite never holding the lock", n)
	}
}

// lockRailOccurrence takes the very lock the announcement takes, by calling the
// production function rather than restating its SQL.
//
// Both halves of that matter. A hand-copied key would stop contending the moment
// storekit changed its spelling, and the test would then pass by holding a lock
// nothing else wants — which is the drift this test exists to catch, reproduced
// inside the test itself. And the WORKSPACE GUC has to be bound first, because
// storekit's key includes current_setting('app.workspace_id'): the first version
// of this helper left it unset, locked a different key, and watched the
// announcement sail past a lock it believed it was holding.
func lockRailOccurrence(t *testing.T, tx pgx.Tx, ws ids.UUID, corr ids.UUID, task ai.Task) error {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`SELECT set_config('app.workspace_id', $1, true)`, ws.String()); err != nil {
		return err
	}
	return storekit.LockWriteIdentity(context.Background(), tx, "ai_task_run", corr.String()+":"+string(task))
}
