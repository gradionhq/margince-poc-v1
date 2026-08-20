// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ---- fakes for the embed-reindex estimate's four ports (no DB; mirrors the
// backfill estimator's own fakes in estimator_test.go, same package) ----

// fakePending fakes search.Store's two fleet-wide rollups (Task 9) over a
// fixed per-workspace map.
type fakePending struct {
	counts map[ids.WorkspaceID]int
	tokens map[ids.WorkspaceID]int64
}

func (f fakePending) PendingByWorkspace(context.Context, string) (map[ids.WorkspaceID]int, error) {
	return f.counts, nil
}

func (f fakePending) TokenSumByWorkspace(context.Context, string) (map[ids.WorkspaceID]int64, error) {
	return f.tokens, nil
}

// fakeEmbedModel fakes ai.Router's embed-lane binding accessor.
type fakeEmbedModel struct {
	ref ai.ModelRef
	ok  bool
}

func (f fakeEmbedModel) CurrentModelForTier(ai.Tier) (ai.ModelRef, bool) { return f.ref, f.ok }

// fakeMonthlyBudget fakes ai.BudgetPolicy.MonthlyTokenBudget at one flat
// figure — the fixtures below never need per-workspace variation.
type fakeMonthlyBudget int64

func (f fakeMonthlyBudget) MonthlyTokenBudget(context.Context, ids.WorkspaceID) (int64, error) {
	return int64(f), nil
}

// fakeSpent fakes ai.Meter.MonthTokens.
type fakeSpent int64

func (f fakeSpent) MonthTokens(context.Context) (int64, error) { return int64(f), nil }

// testWorkspaceID mints a distinct workspace id from a single byte so each
// fixture's fleet is easy to eyeball.
func testWorkspaceID(b byte) ids.WorkspaceID {
	var u ids.UUID
	u[0] = b
	return ids.From[ids.WorkspaceKind](u)
}

// newEmbedEstimator wires the estimator over its fakes; spend is always
// zero across this file's scenarios (each one prices from a clean
// calendar-month baseline), so it is fixed here rather than threaded as
// a parameter every call site would pass the same value for.
func newEmbedEstimator(pending fakePending, rates RateResolver, model fakeEmbedModel, budget fakeMonthlyBudget) *EmbedReindexEstimator {
	return NewEmbedReindexEstimator(pending, rates, model, budget, fakeSpent(0), fixedClock{})
}

// Case A — a workspace priced at a known embed ai_model_rate must carry a
// non-nil costMinor equal to the hand-computed PriceCall figure, and the
// fleet total must fold it in unchanged (one workspace).
func TestEstimateEmbedReindexPricesAtAKnownRate(t *testing.T) {
	ws := testWorkspaceID(1)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{ws: 10},
		tokens: map[ids.WorkspaceID]int64{ws: 1_000_000},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	rates := fakeRates{rateKey("gemini", "embed"): pricedRate}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(10_000_000))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.WorkspaceID != ws {
		t.Fatalf("row.WorkspaceID = %v, want %v", row.WorkspaceID, ws)
	}
	if row.Entities != 10 || row.Tokens != 1_000_000 {
		t.Fatalf("row = %+v, want entities=10 tokens=1000000", row)
	}
	if row.Quality != QualityHeuristic {
		t.Fatalf("Quality = %s, want heuristic (never priced from observed ai_call history)", row.Quality)
	}
	if row.CostMinor == nil {
		t.Fatal("CostMinor = nil, want non-nil (a rate applies)")
	}
	wantMinor := ai.PriceCall(ai.Usage{TokensIn: 1_000_000}, *pricedRate) / microsPerMinor
	if *row.CostMinor != wantMinor {
		t.Fatalf("CostMinor = %d, want %d", *row.CostMinor, wantMinor)
	}
	if row.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", row.Currency)
	}

	if total.Entities != 10 || total.Tokens != 1_000_000 {
		t.Fatalf("total = %+v, want the single workspace's figures folded through unchanged", total)
	}
	if total.CostMinor == nil || *total.CostMinor != wantMinor {
		t.Fatalf("total.CostMinor = %v, want %d", total.CostMinor, wantMinor)
	}
	if total.Quality != QualityHeuristic {
		t.Fatalf("total.Quality = %s, want heuristic", total.Quality)
	}
}

// Case B — a workspace with NO applying ai_model_rate must carry an ABSENT
// (nil) cost, never a fabricated 0 — the never-fabricated-0 posture
// estimator.go already holds for the backfill preview.
func TestEstimateEmbedReindexNoRateIsNilNotZero(t *testing.T) {
	ws := testWorkspaceID(2)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{ws: 5},
		tokens: map[ids.WorkspaceID]int64{ws: 500},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(10_000_000))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if rows[0].CostMinor != nil {
		t.Fatalf("CostMinor = %v, want nil (no ai_model_rate applies — must never fabricate a 0)", rows[0].CostMinor)
	}
	if total.CostMinor != nil {
		t.Fatalf("total.CostMinor = %v, want nil (nothing priced fleet-wide)", total.CostMinor)
	}
	if rows[0].Entities != 5 || rows[0].Tokens != 500 {
		t.Fatalf("row = %+v, want entities=5 tokens=500 (tokens still surfaced when unpriced)", rows[0])
	}
}

// Case C — an empty embed-lane binding (no tier bound at all) must also
// surface a nil cost rather than indexing an absent ModelRef.
func TestEstimateEmbedReindexUnboundEmbedLaneIsNilCost(t *testing.T) {
	ws := testWorkspaceID(3)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{ws: 1},
		tokens: map[ids.WorkspaceID]int64{ws: 100},
	}
	model := fakeEmbedModel{ok: false} // nothing bound
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(10_000_000))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "unbound@0")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if rows[0].CostMinor != nil {
		t.Fatalf("CostMinor = %v, want nil (nothing bound to price against)", rows[0].CostMinor)
	}
	if total.CostMinor != nil {
		t.Fatal("total.CostMinor != nil, want nil")
	}
}

// fakeRatesPerWorkspace prices per workspace, which fakeRates cannot: the
// estimator resolves a rate inside each workspace's own context, and
// ai_model_rate is workspace-scoped under RLS. A workspace absent from the map
// resolves no rate at all, which is the reachable form of two sheets
// disagreeing about one corpus.
type fakeRatesPerWorkspace map[ids.WorkspaceID]fakeRates

var errNoWorkspaceInRateContext = errors.New("costestimate test: rate resolved outside a workspace context")

func (f fakeRatesPerWorkspace) RateFor(ctx context.Context, provider, model string, at time.Time) (*ai.ModelRate, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		// The estimator always resolves a rate inside a workspace context, so
		// an unbound one is a wiring fault in the test rather than a workspace
		// that happens to price nothing.
		return nil, errNoWorkspaceInRateContext
	}
	return f[ids.From[ids.WorkspaceKind](ws)].RateFor(ctx, provider, model, at)
}

// Case D — two workspaces over ONE corpus: the total is what the installation
// will actually rebuild and pay, not the rows added up.
//
// The fixture gives both workspaces the same figures because that is what the
// production reads now return: since ADR-0091 §8 phase D no embeddable entity
// carries a tenant, so `PendingByWorkspace` holds the same rows under every
// workspace it enumerates. Summing them said an installation with two
// workspaces had twice the work at twice the cost, and this figure is what an
// operator reads before confirming the spend.
//
// The assertion is deliberately the arithmetic that must NOT hold — a test
// pinning only the value would pass against the summing version on any
// single-workspace fixture, which is exactly how that version survived.
func TestEstimateEmbedReindexTotalsOneCorpusNotTheSumOfTheRows(t *testing.T) {
	wsA, wsB := testWorkspaceID(4), testWorkspaceID(5)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{wsA: 6, wsB: 6},
		tokens: map[ids.WorkspaceID]int64{wsA: 600_000, wsB: 600_000},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	rates := fakeRates{rateKey("gemini", "embed"): pricedRate}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(1_000_000))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if total.Entities != 6 {
		t.Fatalf("total.Entities = %d, want 6 — the corpus once, not once per workspace", total.Entities)
	}
	if total.Tokens != 600_000 {
		t.Fatalf("total.Tokens = %d, want 600000 — the corpus once", total.Tokens)
	}
	if total.Entities == rows[0].Entities+rows[1].Entities {
		t.Fatalf("total.Entities = %d is the sum of the rows, and the rows are the same corpus", total.Entities)
	}
	wantMinor := ai.PriceCall(ai.Usage{TokensIn: 600_000}, *pricedRate) / microsPerMinor
	if total.CostMinor == nil || *total.CostMinor != wantMinor {
		t.Fatalf("total.CostMinor = %v, want %d (both sheets price the one corpus the same)", total.CostMinor, wantMinor)
	}
}

// The cost is the one figure two workspaces can legitimately disagree on:
// ai_model_rate is workspace-scoped under RLS, so each prices the shared corpus
// against its own sheet. With no single price to state, none is stated — the
// same never-fabricate honesty the per-row field keeps for an unresolvable rate.
func TestEstimateEmbedReindexWithholdsACostTwoRateSheetsDisagreeOn(t *testing.T) {
	wsA, wsB := testWorkspaceID(7), testWorkspaceID(8)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{wsA: 6, wsB: 6},
		tokens: map[ids.WorkspaceID]int64{wsA: 600_000, wsB: 600_000},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	// wsB resolves no rate at all, which is the reachable form of disagreement:
	// one sheet prices the corpus and the other cannot.
	rates := fakeRatesPerWorkspace{wsA: fakeRates{rateKey("gemini", "embed"): pricedRate}}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(1_000_000))

	_, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if total.Entities != 6 {
		t.Fatalf("total.Entities = %d, want 6 — the corpus is the corpus whatever it costs", total.Entities)
	}
	if total.CostMinor != nil {
		t.Fatalf("total.CostMinor = %d, want none: one workspace prices this corpus and the other cannot, "+
			"so there is no installation price to state", *total.CostMinor)
	}
}

// Two sheets that BOTH price the shared corpus, at different rates. This is the
// case the "no single installation price" rule was written for — the sibling
// above only covers one sheet pricing and one unable to, which a rule that
// merely propagated the first non-nil cost would also pass.
func TestEstimateEmbedReindexWithholdsACostTwoSheetsPriceDifferently(t *testing.T) {
	wsA, wsB := testWorkspaceID(9), testWorkspaceID(10)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{wsA: 6, wsB: 6},
		tokens: map[ids.WorkspaceID]int64{wsA: 600_000, wsB: 600_000},
	}
	dearerRate := &ai.ModelRate{InputPerMTokMicroUSD: 9_000_000, OutputPerMTokMicroUSD: 9_000_000}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	rates := fakeRatesPerWorkspace{
		wsA: fakeRates{rateKey("gemini", "embed"): pricedRate},
		wsB: fakeRates{rateKey("gemini", "embed"): dearerRate},
	}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(100_000_000))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	// The premise: the rows really do disagree, so the assertion below is about
	// the rule and not about two rows that happened to match.
	if rows[0].CostMinor == nil || rows[1].CostMinor == nil || *rows[0].CostMinor == *rows[1].CostMinor {
		t.Fatalf("the fixture's two sheets priced the corpus the same (%v, %v); this case needs them to differ",
			rows[0].CostMinor, rows[1].CostMinor)
	}
	if total.CostMinor != nil {
		t.Errorf("total.CostMinor = %d, want none: two sheets price this one corpus differently, "+
			"so there is no installation price — reporting either sheet's figure states one tenant's "+
			"rate as the installation's", *total.CostMinor)
	}
	if total.Entities != 6 {
		t.Errorf("total.Entities = %d, want 6 — the corpus is the corpus whatever it costs", total.Entities)
	}
}

// A workspace with nothing pending and no rate does not withhold the price: it
// has no share of this corpus to disagree about, so the priced rows still speak
// for the installation.
func TestEstimateEmbedReindexIgnoresAnEmptyWorkspaceWithoutARate(t *testing.T) {
	wsA, wsB := testWorkspaceID(11), testWorkspaceID(12)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{wsA: 6, wsB: 0},
		tokens: map[ids.WorkspaceID]int64{wsA: 600_000, wsB: 0},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	rates := fakeRatesPerWorkspace{wsA: fakeRates{rateKey("gemini", "embed"): pricedRate}}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(1_000_000))

	_, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	wantMinor := ai.PriceCall(ai.Usage{TokensIn: 600_000}, *pricedRate) / microsPerMinor
	if total.CostMinor == nil || *total.CostMinor != wantMinor {
		t.Fatalf("total.CostMinor = %v, want %d — an empty workspace has no share to disagree about",
			total.CostMinor, wantMinor)
	}
}

// Case E — utilization_impact discloses the §1.3 band the workspace would
// land in were its share of the estimate added to its current spend —
// reusing ai.BudgetBand exactly (never a forked threshold copy).
func TestEstimateEmbedReindexUtilizationImpactReflectsSpendPlusShare(t *testing.T) {
	ws := testWorkspaceID(6)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{ws: 1},
		tokens: map[ids.WorkspaceID]int64{ws: 850}, // pushes spend from 0 to 850/1000 = 85%
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(1_000))

	rows, _, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if want := ai.BudgetBand(850, 1_000); rows[0].UtilizationImpact != want {
		t.Fatalf("UtilizationImpact = %q, want %q (spent 0 + share 850 against budget 1000)", rows[0].UtilizationImpact, want)
	}
}
