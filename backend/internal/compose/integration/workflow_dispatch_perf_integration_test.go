// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AC-W2 (interfaces.md §5 / PERF-R7 + GATE-CORE-6): trigger→dispatch p95
// must hold under 200ms on the seeded dataset — v1 dropped this
// acceptance criterion silently.
//
// What this measures, precisely: engine.HandleEvent(ctx, env) is the
// exact call the cg:workflows redis subscriber makes once it decodes an
// envelope off the bus (cmd/worker's runSubscriber wraps this same
// method in events.Dedupe). It is synchronous — Match every registered
// handler, Plan the matching ones, claim the (handler, key) row, Apply
// the effect — all DB-bound work whose cost is the variable, dataset-
// dependent quantity the 200ms budget actually targets. Two timestamps
// bracket exactly that call, nothing else.
//
// What this deliberately excludes, and why: the cg:workflows redis
// relay+subscriber transport (event_outbox → platform/events.Relay →
// Redis stream → a Subscriber's XREADGROUP) is NOT measured here.
// compose (this package's home) cannot import the redis client by
// architectural design — .go-arch-lint.yml grants compose only [pgx,
// chi, oapi, river], deliberately no redis, because the bus is a
// cmd/platform concern compose never touches directly. That transport hop is small
// and near-constant (a stream append + a consumer-group read), and it
// is separately proven by internal/platform/events' own bus integration
// test — this is the honest scope of what AC-W2 asks compose to gate,
// not a workaround for an import compose is not allowed to make.
//
// A warmup batch runs first and is discarded: connection setup and
// query-plan caching are fixed costs a firing dispatch never pays in
// steady state, and the search perfbench harness discards them the
// same way (PERF-3/PERF-7).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// workflowDispatchBudget is AC-W2's own budget — a calibration starting
// value in the same spirit as search.Perf3Budget/Perf7Budget: changing
// it is a noted budget revision, never a silent bump.
const workflowDispatchBudget = 200 * time.Millisecond

// leadCreatedEventType names the trigger this suite fires — the one
// seeded starter (route_lead) that reacts to it unconditionally, so
// every sampled lead produces exactly one dispatch to measure.
const leadCreatedEventType = "lead.created"

const (
	dispatchWarmupSamples = 5
	dispatchSampleSize    = 25
)

func TestWorkflowTriggerToDispatchP95HoldsOnTheSeededDataset(t *testing.T) {
	e := Setup(t)
	seedAllStarterAutomations(t, e)
	engine := compose.NewWorkflowEngine(e.Pool)

	// Warmup: discarded, per the file doc comment's fixed-cost note.
	for _, leadID := range seedTriggerLeads(t, e, dispatchWarmupSamples) {
		if err := engine.HandleEvent(context.Background(), leadCreatedEnvelope(e, leadID)); err != nil {
			t.Fatalf("warmup dispatch for lead %s: %v", leadID, err)
		}
	}

	sampleIDs := seedTriggerLeads(t, e, dispatchSampleSize)
	durations := make([]time.Duration, 0, len(sampleIDs))
	for _, leadID := range sampleIDs {
		env := leadCreatedEnvelope(e, leadID)
		start := time.Now()
		if err := engine.HandleEvent(context.Background(), env); err != nil {
			t.Fatalf("dispatching lead %s: %v", leadID, err)
		}
		durations = append(durations, time.Since(start))
	}

	stats, err := search.MeasureQuery("workflow_dispatch", workflowDispatchBudget, durations)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AC-W2 trigger→dispatch [engine.HandleEvent, transport excluded — see file doc comment]: p50=%s p95=%s p99=%s (budget %s, %d samples)",
		stats.P50, stats.P95, stats.P99, stats.Budget, stats.Samples)
	if stats.P95 > workflowDispatchBudget {
		t.Fatalf("AC-W2: trigger→dispatch p95 = %s over the %s budget (%d samples)", stats.P95, workflowDispatchBudget, stats.Samples)
	}
}

// leadCreatedEnvelope builds the delivery engine.HandleEvent would
// receive for leadID's creation — the same envelope shape
// workflow_integration_test.go's own direct-dispatch suites already
// build by hand for this package (TestWorkflowRouteLeadAssignsExactlyOnce),
// reused here rather than re-derived.
func leadCreatedEnvelope(e *Env, leadID ids.UUID) kevents.Envelope {
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        leadCreatedEventType,
		WorkspaceID: e.WS,
		OccurredAt:  time.Now().UTC(),
		Entity:      kevents.EntityRef{Type: "lead", ID: leadID},
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

// seedTriggerLeads creates n leads through the real domain write path
// (people.Store.CreateLead), which stages "lead.created" via
// storekit.Emit in the same transaction as the row — an honest trigger
// entity for Match/Plan/Apply to work against, never a hand-built stand-in.
func seedTriggerLeads(t *testing.T, e *Env, n int) (leadIDs []ids.UUID) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Dispatch Perf Lead %d", i)
		lead, _, err := e.People.CreateLead(e.Admin(), people.CreateLeadInput{FullName: &name, Source: "manual"})
		if err != nil {
			t.Fatalf("seeding trigger lead %d: %v", i, err)
		}
		leadIDs = append(leadIDs, ids.UUID(lead.Id))
	}
	return leadIDs
}
