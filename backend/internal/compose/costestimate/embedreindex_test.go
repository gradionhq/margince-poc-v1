// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

func newEmbedEstimator(pending fakePending, rates fakeRates, model fakeEmbedModel, budget fakeMonthlyBudget, spent fakeSpent) *EmbedReindexEstimator {
	return NewEmbedReindexEstimator(pending, rates, model, budget, spent, fixedClock{})
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
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(10_000_000), fakeSpent(0))

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
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(10_000_000), fakeSpent(0))

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
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(10_000_000), fakeSpent(0))

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

// Case D — two workspaces, only one priced: the fleet total folds BOTH
// workspaces' entities/tokens, but its cost reflects only the priced share
// (never a fabricated cost for the unpriced workspace).
func TestEstimateEmbedReindexFoldsMultipleWorkspacesIntoTotal(t *testing.T) {
	wsA, wsB := testWorkspaceID(4), testWorkspaceID(5)
	pending := fakePending{
		counts: map[ids.WorkspaceID]int{wsA: 4, wsB: 6},
		tokens: map[ids.WorkspaceID]int64{wsA: 400_000, wsB: 600_000},
	}
	model := fakeEmbedModel{ref: ai.ModelRef{Provider: "gemini", Model: "embed"}, ok: true}
	rates := fakeRates{rateKey("gemini", "embed"): pricedRate}
	e := newEmbedEstimator(pending, rates, model, fakeMonthlyBudget(1_000_000), fakeSpent(0))

	rows, total, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if total.Entities != 10 {
		t.Fatalf("total.Entities = %d, want 10 (4+6 folded)", total.Entities)
	}
	if total.Tokens != 1_000_000 {
		t.Fatalf("total.Tokens = %d, want 1000000 (400000+600000 folded)", total.Tokens)
	}
	wantAMinor := ai.PriceCall(ai.Usage{TokensIn: 400_000}, *pricedRate) / microsPerMinor
	wantBMinor := ai.PriceCall(ai.Usage{TokensIn: 600_000}, *pricedRate) / microsPerMinor
	if total.CostMinor == nil || *total.CostMinor != wantAMinor+wantBMinor {
		t.Fatalf("total.CostMinor = %v, want %d", total.CostMinor, wantAMinor+wantBMinor)
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
	e := newEmbedEstimator(pending, fakeRates{}, model, fakeMonthlyBudget(1_000), fakeSpent(0))

	rows, _, err := e.EstimateEmbedReindex(context.Background(), "gemini/embed@1024")
	if err != nil {
		t.Fatalf("EstimateEmbedReindex: %v", err)
	}
	if want := ai.BudgetBand(850, 1_000); rows[0].UtilizationImpact != want {
		t.Fatalf("UtilizationImpact = %q, want %q (spent 0 + share 850 against budget 1000)", rows[0].UtilizationImpact, want)
	}
}
