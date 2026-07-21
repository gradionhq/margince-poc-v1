// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ---- fakes for the five ports + the clock (no DB, no real clock) ----

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type fakeTotals struct {
	got   []ai.Task // the tasks the estimator asked for
	since time.Time
	rows  []ai.ServedTaskTotal
}

func (f *fakeTotals) ServedTaskTotals(_ context.Context, tasks []ai.Task, since time.Time) ([]ai.ServedTaskTotal, error) {
	f.got = tasks
	f.since = since
	return f.rows, nil
}

// fakeRates prices (provider, model) pairs present in the map; absent ⇒ unpriced
// (nil, nil), the ai.RateStore contract.
type fakeRates map[string]*ai.ModelRate

func rateKey(provider, model string) string { return provider + "|" + model }

func (f fakeRates) RateFor(_ context.Context, provider, model string, _ time.Time) (*ai.ModelRate, error) {
	return f[rateKey(provider, model)], nil
}

// fakeLadder mimics ai.Router's binding resolvers over a fixed tier→ref map and
// per-task tier ladders.
type fakeLadder struct {
	tiers   map[ai.Tier]ai.ModelRef
	ladders map[ai.Task][]ai.Tier
}

func (f fakeLadder) BoundLadder(task ai.Task) []ai.ModelRef {
	var out []ai.ModelRef
	for _, tier := range f.ladders[task] {
		if ref, ok := f.CurrentModelForTier(tier); ok {
			out = append(out, ref)
		}
	}
	return out
}

func (f fakeLadder) CurrentModelForTier(tier ai.Tier) (ai.ModelRef, bool) {
	ref, ok := f.tiers[tier]
	return ref, ok
}

type fakeLabels int64

func (f fakeLabels) LabeledCaptureCountSince(context.Context, time.Time) (int64, error) {
	return int64(f), nil
}

type fakeYields capture.BackfillYields

func (f fakeYields) BackfillYields(context.Context, string, ids.UserID) (capture.BackfillYields, error) {
	return capture.BackfillYields(f), nil
}

// defaultLadders binds each backfill task to its real tier ladder so the fakes
// mirror the router's routing table.
func defaultLadders() map[ai.Task][]ai.Tier {
	return map[ai.Task][]ai.Tier{
		ai.TaskCaptureClassify: {ai.TierLocalSmall, ai.TierCheapCloud},
		ai.TaskEnrich:          {ai.TierLocalSmall, ai.TierCheapCloud},
		ai.TaskEmbeddings:      {ai.TierEmbedLane},
	}
}

// priced is a nonzero input-only rate; zeroRate is a real $0 rate (present, so
// it prices — distinct from unpriced/absent).
var (
	pricedRate = &ai.ModelRate{InputPerMTokMicroUSD: 1_000_000, OutputPerMTokMicroUSD: 2_000_000}
	zeroRate   = &ai.ModelRate{}
)

func newEstimator(totals *fakeTotals, rates fakeRates, ladder fakeLadder, labels fakeLabels, yields fakeYields) *Estimator {
	clock := fixedClock{t: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	return NewEstimator(totals, rates, ladder, labels, yields, clock)
}

func mustEstimate(t *testing.T, e *Estimator, scanned int64) BackfillCost {
	t.Helper()
	got, err := e.EstimateBackfill(context.Background(), "gmail", ids.New[ids.UserKind](), scanned)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	return got
}

// Case A — observed, single bound model per task, all priced (classify at a
// nonzero rate, enrich/embed at real $0 rates so they stay observed but add no
// cost). The estimate is fully observed, and classify's cost is exactly the
// hand-computed per-unit-priced figure scaled to the expected units.
func TestEstimateObservedSingleBoundModel(t *testing.T) {
	const scanned = 200
	classify := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "flash",
		TokensIn: 1_000_000, TokensOut: 100_000, Calls: 5,
	}
	enrich := ai.ServedTaskTotal{
		Task: ai.TaskEnrich, Tier: ai.TierLocalSmall, Provider: "ollama", ModelID: "gemma3",
		TokensIn: 300, TokensOut: 40, Calls: 4,
	}
	embed := ai.ServedTaskTotal{
		Task: ai.TaskEmbeddings, Tier: ai.TierEmbedLane, Provider: "gemini", ModelID: "embed",
		TokensIn: 500, Calls: 10,
	}
	rates := fakeRates{
		rateKey("gemini", "flash"):  pricedRate,
		rateKey("ollama", "gemma3"): zeroRate,
		rateKey("gemini", "embed"):  zeroRate,
	}
	ladder := fakeLadder{
		tiers: map[ai.Tier]ai.ModelRef{
			ai.TierLocalSmall: {Provider: "ollama", Model: "gemma3"},
			ai.TierCheapCloud: {Provider: "gemini", Model: "flash"},
			ai.TierEmbedLane:  {Provider: "gemini", Model: "embed"},
		},
		ladders: defaultLadders(),
	}
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{classify, enrich, embed}}
	yields := fakeYields{Scanned: 200, Captured: 200, PeopleCreated: 20}
	e := newEstimator(totals, rates, ladder, fakeLabels(50), yields)

	got := mustEstimate(t, e, scanned)

	// classify units = scanned × captured/scanned = 200; denom = labeled = 50;
	// pricedDenom = 50×5/5 = 50; cost = PriceCall(classify) × 200 / 50.
	classifyMicro := ai.PriceCall(usageOf(classify), *pricedRate) * 200 / 50
	wantMinor := classifyMicro / microsPerMinor // enrich + embed price at $0
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (a priced slice exists)")
	}
	if got.Quality != QualityObserved {
		t.Fatalf("Quality = %s, want observed (every task priced from real history + yields)", got.Quality)
	}
	if got.CostMinor != wantMinor {
		t.Fatalf("CostMinor = %d, want %d (classify priced, enrich/embed $0)", got.CostMinor, wantMinor)
	}
	if got.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", got.Currency)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0", got.InputTokens)
	}
	// The window is the trailing 7 days of the injected clock.
	if want := e.clock.Now().Add(-estimateWindow); !totals.since.Equal(want) {
		t.Fatalf("since = %v, want %v (7-day window)", totals.since, want)
	}
}

// Case B — rebind reprice. The served cheap_cloud model has DEPARTED the ladder;
// cheap_cloud now binds a different model with a rate. The departed slice must
// reprice at the tier's CURRENT binding, never the $0 local ladder head — a
// routine cheap_cloud swap must not vanish the projected cloud cost.
func TestEstimateRepricesDepartedSliceAtItsTiersCurrentModel(t *testing.T) {
	const scanned = 100
	// Only classify is observed here; enrich/embed have no slices and no bound
	// rate, so they add nothing to cost.
	served := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "old-flash",
		TokensIn: 1_000_000, TokensOut: 100_000, Calls: 3,
	}
	ladder := fakeLadder{
		tiers: map[ai.Tier]ai.ModelRef{
			ai.TierLocalSmall: {Provider: "ollama", Model: "gemma3"},    // the $0 head
			ai.TierCheapCloud: {Provider: "gemini", Model: "new-flash"}, // the new binding
		},
		ladders: defaultLadders(),
	}
	rates := fakeRates{
		rateKey("ollama", "gemma3"):    zeroRate,   // would vanish the cost if used
		rateKey("gemini", "new-flash"): pricedRate, // the correct reprice target
		// "old-flash" has no rate — but it must never be the priced model anyway.
	}
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{served}}
	yields := fakeYields{Scanned: 100, Captured: 100}
	e := newEstimator(totals, rates, ladder, fakeLabels(50), yields)

	got := mustEstimate(t, e, scanned)

	// units = 100; denom = 50; pricedDenom = 50×3/3 = 50; priced at new-flash.
	wantMicro := ai.PriceCall(usageOf(served), *pricedRate) * 100 / 50
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d (repriced at cheap_cloud's NEW binding, not the $0 head)", got.CostMinor, wantMicro/microsPerMinor)
	}
	if !got.HasCost || got.CostMinor <= 0 {
		t.Fatalf("cost vanished (%d, hasCost=%v) — the departed slice was mispriced at the $0 local head", got.CostMinor, got.HasCost)
	}
}

// Case C — denom == 0 (a classify week with slices but no labeled messages)
// routes classify to the floor: no div-by-zero, quality heuristic.
func TestEstimateZeroDenominatorRoutesToFloorWithoutDivByZero(t *testing.T) {
	served := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "flash",
		TokensIn: 1000, TokensOut: 100, Calls: 5,
	}
	ladder := fakeLadder{
		tiers:   map[ai.Tier]ai.ModelRef{ai.TierCheapCloud: {Provider: "gemini", Model: "flash"}},
		ladders: defaultLadders(),
	}
	rates := fakeRates{rateKey("gemini", "flash"): pricedRate}
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{served}}
	e := newEstimator(totals, rates, ladder, fakeLabels(0), fakeYields{Scanned: 100, Captured: 100})

	got := mustEstimate(t, e, 100) // must not panic
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (classify fell to the floor at denom==0)", got.Quality)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0 (floor tokens still surfaced)", got.InputTokens)
	}
}

// Case D — empty ladder. No tier bound at all → every slice unpriced, tokens
// surfaced, HasCost=false, no [0] index panic.
func TestEstimateEmptyLadderSurfacesTokensUnpriced(t *testing.T) {
	served := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "flash",
		TokensIn: 1000, TokensOut: 100, Calls: 5,
	}
	ladder := fakeLadder{tiers: map[ai.Tier]ai.ModelRef{}, ladders: defaultLadders()} // nothing bound
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{served}}
	e := newEstimator(totals, fakeRates{}, ladder, fakeLabels(50), fakeYields{Scanned: 100, Captured: 100})

	got := mustEstimate(t, e, 100) // must not panic on an empty BoundLadder
	if got.HasCost {
		t.Fatal("HasCost = true, want false (nothing is bound to price against)")
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic", got.Quality)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0 (tokens surfaced even when unpriced)", got.InputTokens)
	}
}

// Case E — all-unpriced. Slices are bound to real models, but no rate resolves
// → HasCost=false, quality heuristic, tokens still surfaced.
func TestEstimateAllUnpricedSurfacesTokensNoCost(t *testing.T) {
	served := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "flash",
		TokensIn: 1000, TokensOut: 100, Calls: 5,
	}
	ladder := fakeLadder{
		tiers:   map[ai.Tier]ai.ModelRef{ai.TierCheapCloud: {Provider: "gemini", Model: "flash"}},
		ladders: defaultLadders(),
	}
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{served}}
	e := newEstimator(totals, fakeRates{}, ladder, fakeLabels(50), fakeYields{Scanned: 100, Captured: 100})

	got := mustEstimate(t, e, 100)
	if got.HasCost {
		t.Fatal("HasCost = true, want false (every RateFor returned nil)")
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic", got.Quality)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0 (input surfaced despite being unpriced)", got.InputTokens)
	}
}

// Case F — units defaulted. Yields are zero-value (no completed run) → units
// fall to the floor and quality is heuristic, but the OBSERVED per-unit cost is
// still applied (the live-sync-then-first-backfill case is NOT all-floor).
func TestEstimateDefaultedUnitsStillApplyObservedCost(t *testing.T) {
	served := ai.ServedTaskTotal{
		Task: ai.TaskCaptureClassify, Tier: ai.TierCheapCloud, Provider: "gemini", ModelID: "flash",
		TokensIn: 1_000_000, TokensOut: 100_000, Calls: 5,
	}
	ladder := fakeLadder{
		tiers:   map[ai.Tier]ai.ModelRef{ai.TierCheapCloud: {Provider: "gemini", Model: "flash"}},
		ladders: defaultLadders(),
	}
	rates := fakeRates{rateKey("gemini", "flash"): pricedRate}
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{served}}
	e := newEstimator(totals, rates, ladder, fakeLabels(50), fakeYields{}) // zero-value yields

	got := mustEstimate(t, e, 100)
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (observed per-unit cost still applies)")
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (units were defaulted to the floor)", got.Quality)
	}
	if got.CostMinor <= 0 {
		t.Fatalf("CostMinor = %d, want > 0 (observed cost survives defaulted units)", got.CostMinor)
	}
	// classify floor units = scanned = 100; observed cost per unit still used.
	wantMicro := ai.PriceCall(usageOf(served), *pricedRate) * unitsFloor(ai.TaskCaptureClassify, 100) / 50
	if got.CostMinor != wantMicro/microsPerMinor {
		t.Fatalf("CostMinor = %d, want %d (observed cost × floor units)", got.CostMinor, wantMicro/microsPerMinor)
	}
}

// v3.1 degenerate: pricedDenom is floored ≥ 1 — a priced slice whose computed
// share of the denominator rounds to 0 (one priced call amid a large unpriced
// call count) must not divide by zero.
func TestEstimatePricedDenomFlooredAtOne(t *testing.T) {
	priced := ai.ServedTaskTotal{
		Task: ai.TaskEnrich, Tier: ai.TierLocalSmall, Provider: "ollama", ModelID: "gemma3",
		TokensIn: 300, TokensOut: 40, Calls: 1,
	}
	// A huge unpriced slice at the same task drives sumCalls up so that
	// denom(=Σcalls)×pricedCalls/sumCalls truncates toward 0 before the floor.
	unpriced := ai.ServedTaskTotal{
		Task: ai.TaskEnrich, Tier: ai.TierLocalSmall, Provider: "custom", ModelID: "no-rate",
		TokensIn: 9_000, TokensOut: 900, Calls: 999,
	}
	ladder := fakeLadder{
		tiers:   map[ai.Tier]ai.ModelRef{ai.TierLocalSmall: {Provider: "ollama", Model: "gemma3"}},
		ladders: defaultLadders(),
	}
	rates := fakeRates{rateKey("ollama", "gemma3"): pricedRate} // "custom/no-rate" unpriced
	totals := &fakeTotals{rows: []ai.ServedTaskTotal{priced, unpriced}}
	// enrich denom = Σcalls = 1000; pricedCalls = 1; 1000×1/1000 = 1 → floored ≥1.
	e := newEstimator(totals, rates, ladder, fakeLabels(0), fakeYields{Scanned: 100, Captured: 100, PeopleCreated: 50})

	got := mustEstimate(t, e, 100) // must not panic (div-by-zero guard)
	if !got.HasCost {
		t.Fatal("HasCost = false, want true (the ollama slice priced)")
	}
	if got.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (an unpriced slice is in the mix)", got.Quality)
	}
}

// The served-row window is anchored on the injected clock, and the estimator
// asks for exactly the three backfill tasks — nothing broader.
func TestEstimateAsksForTheBackfillTasksOverTheWindow(t *testing.T) {
	totals := &fakeTotals{}
	ladder := fakeLadder{tiers: map[ai.Tier]ai.ModelRef{}, ladders: defaultLadders()}
	e := newEstimator(totals, fakeRates{}, ladder, fakeLabels(0), fakeYields{})
	_ = mustEstimate(t, e, 10)

	if len(totals.got) != len(backfillTasks) {
		t.Fatalf("asked for %v, want %v", totals.got, backfillTasks)
	}
	for i, task := range backfillTasks {
		if totals.got[i] != task {
			t.Fatalf("task[%d] = %s, want %s", i, totals.got[i], task)
		}
	}
}
