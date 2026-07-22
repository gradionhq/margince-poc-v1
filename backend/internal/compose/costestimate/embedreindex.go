// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

// The embed-reindex advisory cost estimate (ADR-0068, "the scope before the
// spend" for /embeddings/reindex/preview): per-workspace rows plus a fleet
// total, priced from Task 9's fleet-wide pending rollups
// (search.Store.PendingByWorkspace / TokenSumByWorkspace) against the
// CURRENT embed-lane binding's ai_model_rate. The embed lane is
// budget-EXEMPT (routing never queues or degrades it) — this estimate is
// consent-before-spend disclosure, never a gate (ADR-0020, NEVER-4): an
// unrated binding suppresses the cost field, it never reads as a free $0.
//
// estimate_quality is always "heuristic": TokenSumByWorkspace is a
// SUM(length(source text))/4 work-shape figure, not an observed
// per-embedding-call billing line (there is no such line to observe), so
// this estimator — unlike EstimateBackfill's mixed observed/heuristic
// posture — never has an "observed" path.

import (
	"context"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Row is one priced line of the embed-reindex estimate — either a single
// workspace's share or the fleet total (WorkspaceID left at its zero value,
// UtilizationImpact left "" — no single monthly budget applies fleet-wide).
type Row struct {
	WorkspaceID ids.WorkspaceID
	Entities    int
	Tokens      int64
	// CostMinor is nil when no ai_model_rate applies to the current embed
	// binding — absent, never a fabricated 0 (price-on-read, ADR-0067).
	CostMinor *int64
	Currency  string // "USD" in v1, always set regardless of CostMinor
	Quality   Quality
	// UtilizationImpact is the §1.3 band (ai.BandNormal|Degraded|Queued)
	// this workspace would land in were Tokens added to its current
	// calendar-month spend — a disclosure only; the embed lane itself never
	// queues or degrades on it.
	UtilizationImpact string
}

// PendingReader is search.Store's two fleet-wide rollups (Task 9): every
// live, non-empty-text embeddable entity that lacks a current-identity
// embedding row, counted and token-summed per workspace.
type PendingReader interface {
	PendingByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, error)
	TokenSumByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int64, error)
}

// EmbedModelResolver is ai.Router's embed-lane binding accessor, narrowed to
// exactly what this estimate prices against — the (provider, model)
// TokenSumByWorkspace's pending token sum would actually be embedded at,
// were a reindex started now.
type EmbedModelResolver interface {
	CurrentModelForTier(tier ai.Tier) (ai.ModelRef, bool)
}

// BudgetReader is ai.BudgetPolicy.MonthlyTokenBudget — the workspace's
// calendar-month token pool, the utilization_impact denominator.
type BudgetReader interface {
	MonthlyTokenBudget(ctx context.Context, workspaceID ids.WorkspaceID) (int64, error)
}

// SpentReader is ai.Meter.MonthTokens — the workspace's calendar-month spend
// so far, added to its share of the estimate before banding.
type SpentReader interface {
	MonthTokens(ctx context.Context) (int64, error)
}

// EmbedReindexEstimator composes the fleet-wide pending rollups with the
// current embed binding's price and every workspace's budget position into
// the advisory embed-reindex estimate.
type EmbedReindexEstimator struct {
	pending PendingReader
	rates   RateResolver
	model   EmbedModelResolver
	budget  BudgetReader
	spent   SpentReader
	clock   Clock
}

// NewEmbedReindexEstimator wires the estimator over its five reads and an
// injected clock (no time.Now() in the estimator; T11).
func NewEmbedReindexEstimator(pending PendingReader, rates RateResolver, model EmbedModelResolver,
	budget BudgetReader, spent SpentReader, clock Clock,
) *EmbedReindexEstimator {
	return &EmbedReindexEstimator{pending: pending, rates: rates, model: model, budget: budget, spent: spent, clock: clock}
}

// EstimateEmbedReindex prices what starting a fleet-wide reindex now would
// touch: one Row per live tenant workspace carrying its pending entities,
// pending tokens, priced cost (nil when unrated), and budget-impact
// disclosure, plus the fleet total folding every workspace's figures.
// currentIdentity is the live embed binding's provider/model@dims (Task 9) —
// the same string PendingByWorkspace/TokenSumByWorkspace key their pending
// set on. Every port read propagates its error (never swallowed).
func (e *EmbedReindexEstimator) EstimateEmbedReindex(ctx context.Context, currentIdentity string) (perWorkspace []Row, total Row, err error) {
	counts, err := e.pending.PendingByWorkspace(ctx, currentIdentity)
	if err != nil {
		return nil, Row{}, err
	}
	tokens, err := e.pending.TokenSumByWorkspace(ctx, currentIdentity)
	if err != nil {
		return nil, Row{}, err
	}

	// The embed lane binds ONE (provider, model) fleet-wide — resolved once,
	// not per workspace, unlike the rate below which is a workspace-scoped
	// ai_model_rate row (RLS: each tenant prices its own sheet).
	ref, bound := e.model.CurrentModelForTier(ai.TierEmbedLane)
	today := e.clock.Now()

	perWorkspace = make([]Row, 0, len(counts))
	var totalEntities int
	var totalTokens, totalCostMinor int64
	var totalHasCost bool

	for _, wsID := range sortedWorkspaceIDs(counts) {
		row, err := e.priceWorkspaceRow(ctx, wsID, counts[wsID], tokens[wsID], ref, bound, today)
		if err != nil {
			return nil, Row{}, err
		}
		perWorkspace = append(perWorkspace, row)

		totalEntities += row.Entities
		totalTokens += row.Tokens
		if row.CostMinor != nil {
			totalCostMinor += *row.CostMinor
			totalHasCost = true
		}
	}

	total = Row{
		Entities: totalEntities,
		Tokens:   totalTokens,
		Currency: currencyUSD,
		Quality:  QualityHeuristic,
	}
	if totalHasCost {
		total.CostMinor = &totalCostMinor
	}
	return perWorkspace, total, nil
}

// priceWorkspaceRow prices one workspace's share of the estimate: its pending
// entities/tokens (already read fleet-wide), the cost at the current embed
// binding's rate (nil when unbound or unrated — never fabricated as 0), and
// the budget band its share would push it into. ref/bound/today are resolved
// once by the caller (the embed binding and the clock read are fleet-wide,
// not per-workspace).
func (e *EmbedReindexEstimator) priceWorkspaceRow(ctx context.Context, wsID ids.WorkspaceID, entities int, wsTokens int64,
	ref ai.ModelRef, bound bool, today time.Time,
) (Row, error) {
	wsCtx := systemWorkspaceContext(ctx, wsID.UUID)

	var costMinor *int64
	if bound {
		rate, err := e.rates.RateFor(wsCtx, ref.Provider, ref.Model, today)
		if err != nil {
			return Row{}, err
		}
		if rate != nil {
			minor := ai.PriceCall(ai.Usage{TokensIn: int(wsTokens)}, *rate) / microsPerMinor
			costMinor = &minor
		}
	}

	monthly, err := e.budget.MonthlyTokenBudget(wsCtx, wsID)
	if err != nil {
		return Row{}, err
	}
	spent, err := e.spent.MonthTokens(wsCtx)
	if err != nil {
		return Row{}, err
	}

	return Row{
		WorkspaceID:       wsID,
		Entities:          entities,
		Tokens:            wsTokens,
		CostMinor:         costMinor,
		Currency:          currencyUSD,
		Quality:           QualityHeuristic,
		UtilizationImpact: ai.BudgetBand(spent+wsTokens, monthly),
	}, nil
}

// sortedWorkspaceIDs orders counts' keys deterministically: counts/tokens
// arrive as maps (fleet enumeration order is not preserved through them),
// and a stable per-workspace ordering is what makes this estimate's output
// reproducible run to run.
func sortedWorkspaceIDs(counts map[ids.WorkspaceID]int) []ids.WorkspaceID {
	workspaceIDs := make([]ids.WorkspaceID, 0, len(counts))
	for wsID := range counts {
		workspaceIDs = append(workspaceIDs, wsID)
	}
	sort.Slice(workspaceIDs, func(i, j int) bool {
		return workspaceIDs[i].String() < workspaceIDs[j].String()
	})
	return workspaceIDs
}

// systemWorkspaceContext binds ctx to wsID under the system principal — the
// same posture search/binding.go's own fleet-wide rollup (pendingStats)
// uses: a per-workspace ai_model_rate/budget/spend read during index
// maintenance must see the workspace regardless of any one caller's row
// scope. The actual GUC bind happens inside RateFor/MonthlyTokenBudget/
// MonthTokens themselves (database.WithWorkspaceTx), keyed off the
// workspace id this context now carries.
func systemWorkspaceContext(ctx context.Context, wsID ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, wsID)
	return principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: systemPrincipalID})
}

// systemPrincipalID names the system actor this fleet-wide estimate reads
// as — mirrors search/binding.go's own constant of the same name and value
// (the two packages never import one another, so each names it once).
const systemPrincipalID = "system"
