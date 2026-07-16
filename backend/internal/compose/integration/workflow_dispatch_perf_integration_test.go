// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AC-W2 (interfaces.md §5 / PERF-R7 + GATE-CORE-6): trigger→dispatch p95
// must hold under 200ms on the seeded dataset, through cg:workflows —
// v1 dropped this acceptance criterion silently. This suite proves it
// over the REAL pipeline a production firing rides: a domain write
// stages a trigger event in event_outbox, platform/events.Relay ships it
// to Redis, a cg:workflows Subscriber (the same group cmd/worker
// consumes, kevents.Groups()) delivers it through events.Dedupe, and the
// automation engine dispatches it — nothing here is a hand-built stand-in
// for what the bus actually carries.
//
// Two timestamps per sampled lead: t0 is read the instant
// people.Store.CreateLead's transaction committed (the earliest moment
// the "lead.created" trigger genuinely exists in event_outbox); t1 is
// read the instant the wrapped cg:workflows handler returns from
// engine.HandleEvent for that same envelope (env.Entity.ID names the
// lead, so completions correlate without touching event ids the test
// never sees). p95 across the sample is asserted against the 200ms
// budget with search.MeasureQuery (internal/modules/search/perfbench.go)
// — the same percentile machinery PERF-3/PERF-7 already use, reused
// rather than re-derived.
//
// Arrival pattern, stated precisely: the sample's leads are all created
// BEFORE the sample-phase relay+subscriber pair starts, so its very
// first relay pass (which never waits — Relay.Run only backs off AFTER
// a pass finds nothing) drains the whole batch immediately instead of
// waiting out the relay's steady-state 200ms idle-poll interval. That
// interval is a real, separate deployment knob (NewRelay's default): a
// single event trickling in between polls can wait up to the full
// interval before Relay even looks for it, which would make ANY
// interval >= the 200ms budget dominate this measurement with a number
// that says nothing about the workflow engine's own dispatch speed. The
// seeded-batch framing isolates what AC-W2 actually names — cg:workflows
// dispatch cost once an event has reached the bus — and is reported
// as exactly that, not as a claim about sporadic single-event arrival
// latency (see task-16-report.md's concerns section).
//
// A warmup batch runs first and is discarded (its own fresh relay+
// subscriber pair, stopped before the timed batch starts): connection
// setup, consumer-group creation, and query-plan caching are fixed
// costs a firing dispatch never pays in steady state, and the search
// perfbench harness discards them the same way.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// workflowDispatchBudget is AC-W2's own budget — a calibration starting
// value in the same spirit as search.Perf3Budget/Perf7Budget: changing
// it is a noted budget revision, never a silent bump.
const workflowDispatchBudget = 200 * time.Millisecond

// leadCreatedEventType names the trigger this suite fires — the one
// seeded starter (route_lead) that reacts to it unconditionally, so
// every sampled lead produces exactly one dispatch to correlate against.
const leadCreatedEventType = "lead.created"

const (
	dispatchWarmupSamples = 5
	dispatchSampleSize    = 25
	dispatchAwaitDeadline = 30 * time.Second
)

// workflowPerfRedisDB is a logical Redis db distinct from the events
// package's own (bus_integration_test.go's testDB, 15): the parallel
// integration runner (scripts/test-integration-parallel.sh) passes one
// shared Redis instance to every package, documented as safe only
// because "only the events package uses Redis". This suite is the
// second, so it must not share a db with the first — two packages
// FlushDB-ing and declaring identical stream/group keys on the same db
// concurrently would corrupt each other's runs.
const workflowPerfRedisDB = 14

// dispatchSignal is one observed cg:workflows completion: which entity
// fired and when the wrapped handler finished dispatching it.
type dispatchSignal struct {
	entity ids.UUID
	at     time.Time
}

func TestWorkflowTriggerToDispatchP95HoldsOnTheSeededDataset(t *testing.T) {
	e := Setup(t)
	seedAllStarterAutomations(t, e)

	rdb := testRedisClient(t)
	group := cgWorkflowsGroup(t)
	engine := compose.NewWorkflowEngine(e.Pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	completions := make(chan dispatchSignal, dispatchWarmupSamples+dispatchSampleSize)
	handler := func(ctx context.Context, env kevents.Envelope) error {
		err := engine.HandleEvent(ctx, env)
		if err == nil && env.Type == leadCreatedEventType {
			completions <- dispatchSignal{entity: env.Entity.ID, at: time.Now()}
		}
		return err
	}

	// Warmup: a fresh pipeline pair, drained and stopped before the
	// timed batch — its cost is fixed setup, not per-event dispatch.
	warmupCtx, cancelWarmup := context.WithCancel(context.Background())
	stopWarmup := startDispatchPipeline(t, warmupCtx, e.Pool, rdb, group, handler, logger)
	warmupIDs, _ := seedTriggerLeads(t, e, dispatchWarmupSamples)
	awaitCtx, cancelAwait := context.WithTimeout(context.Background(), dispatchAwaitDeadline)
	awaitDispatches(awaitCtx, t, completions, idSet(warmupIDs))
	cancelAwait()
	cancelWarmup()
	stopWarmup()

	// Sample: staged while nothing is consuming, THEN a fresh pipeline
	// pair starts — its first relay pass drains the whole batch with no
	// preceding idle wait (see the file doc comment's arrival-pattern note).
	sampleIDs, startAt := seedTriggerLeads(t, e, dispatchSampleSize)
	sampleCtx, cancelSample := context.WithCancel(context.Background())
	stopSample := startDispatchPipeline(t, sampleCtx, e.Pool, rdb, group, handler, logger)
	awaitCtx, cancelAwait = context.WithTimeout(context.Background(), dispatchAwaitDeadline)
	completedAt := awaitDispatches(awaitCtx, t, completions, idSet(sampleIDs))
	cancelAwait()
	cancelSample()
	stopSample()

	durations := make([]time.Duration, 0, len(sampleIDs))
	for _, id := range sampleIDs {
		durations = append(durations, completedAt[id].Sub(startAt[id]))
	}
	stats, err := search.MeasureQuery("workflow_dispatch", workflowDispatchBudget, durations)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AC-W2 trigger→dispatch [seeded dataset, through cg:workflows]: p50=%s p95=%s p99=%s (budget %s, %d samples)",
		stats.P50, stats.P95, stats.P99, stats.Budget, stats.Samples)
	if stats.P95 > workflowDispatchBudget {
		t.Fatalf("AC-W2: trigger→dispatch p95 = %s over the %s budget (%d samples)", stats.P95, workflowDispatchBudget, stats.Samples)
	}
}

// seedAllStarterAutomations enrolls the six seeded starter templates the
// way a fresh workspace's bootstrap does (SeedStarterAutomationsTx) —
// "the seeded dataset" AC-W2 names, not a hand-picked single instance.
func seedAllStarterAutomations(t *testing.T, e *Env) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return automation.SeedStarterAutomationsTx(context.Background(), tx)
	})
	if err != nil {
		t.Fatalf("seeding starter automations: %v", err)
	}
}

// testRedisClient opens the isolated logical db this suite owns,
// flushed clean, mirroring bus_integration_test.go's own isolation
// (distinct db number — see workflowPerfRedisDB).
func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("MARGINCE_TEST_REDIS")
	if addr == "" {
		t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: workflowPerfRedisDB})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis at %s unreachable — run `make db-up`: %v", addr, err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushing test redis db: %v", err)
	}
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("closing redis client: %v", err)
		}
	})
	return rdb
}

// cgWorkflowsGroup resolves the REAL production group definition
// (kevents.Groups(), the same catalog cmd/worker's runSubscriber reads)
// rather than hand-rolling a stand-in that could drift from what the
// worker actually subscribes.
func cgWorkflowsGroup(t *testing.T) kevents.Group {
	t.Helper()
	for _, g := range kevents.Groups() {
		if g.Name == "cg:workflows" {
			return g
		}
	}
	t.Fatal("cg:workflows group not declared in the event catalog")
	return kevents.Group{}
}

// startDispatchPipeline runs a fresh outbox relay + cg:workflows
// subscriber pair over ctx — the real outbox→relay→Redis→engine chain,
// dedupe-wrapped exactly as cmd/worker wires it. The returned stop func
// blocks until BOTH background goroutines have actually exited, so a
// caller starting a second pipeline right after never races the first.
func startDispatchPipeline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, group kevents.Group, handler events.Handler, logger *slog.Logger) (stop func()) {
	t.Helper()
	relay := events.NewRelay(pool, rdb, logger)
	sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, handler), logger)

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.Run(ctx)
	}()

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("cg:workflows subscriber exited: %v", err)
		}
	}()

	return func() {
		<-subDone
		<-relayDone
	}
}

// seedTriggerLeads creates n leads through the real domain write path
// (people.Store.CreateLead), which stages "lead.created" via
// storekit.Emit in the SAME transaction as the row — an honest trigger,
// never a hand-built envelope. startAt[id] is read the instant CreateLead
// returns: the earliest externally observable moment the trigger exists.
func seedTriggerLeads(t *testing.T, e *Env, n int) (leadIDs []ids.UUID, startAt map[ids.UUID]time.Time) {
	t.Helper()
	startAt = make(map[ids.UUID]time.Time, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Dispatch Perf Lead %d", i)
		lead, _, err := e.People.CreateLead(e.Admin(), people.CreateLeadInput{FullName: &name, Source: "manual"})
		if err != nil {
			t.Fatalf("seeding trigger lead %d: %v", i, err)
		}
		at := time.Now()
		id := ids.UUID(lead.Id)
		leadIDs = append(leadIDs, id)
		startAt[id] = at
	}
	return leadIDs, startAt
}

// idSet turns a slice of entity ids into the membership set
// awaitDispatches matches completions against.
func idSet(entities []ids.UUID) map[ids.UUID]struct{} {
	set := make(map[ids.UUID]struct{}, len(entities))
	for _, id := range entities {
		set[id] = struct{}{}
	}
	return set
}

// awaitDispatches blocks until every id in want has produced a
// completion signal or ctx's deadline fires — the channel is the only
// wake source, the same subscribe-then-block pattern
// jobs_integration_test.go's awaitKindCompleted uses for River: no
// polling, no time.Sleep, so the recorded durations are never inflated
// by a poll tick.
func awaitDispatches(ctx context.Context, t *testing.T, ch <-chan dispatchSignal, want map[ids.UUID]struct{}) map[ids.UUID]time.Time {
	t.Helper()
	got := make(map[ids.UUID]time.Time, len(want))
	for len(got) < len(want) {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for dispatch completion: %d/%d leads dispatched (%v)", len(got), len(want), ctx.Err())
		case sig := <-ch:
			if _, wanted := want[sig.entity]; wanted {
				got[sig.entity] = sig.at
			}
		}
	}
	return got
}
