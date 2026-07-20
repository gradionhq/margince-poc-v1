// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package ai

// ADR-0067 phase 1's real-Postgres proof: RateFor's as-of-date resolution
// across a rate change, CostReport's unpriced counting and free-by-
// construction exclusions (cache_hit, zero-usage), a row-by-row
// cross-check of the aggregate SQL against PriceCall on identical fixture
// data, and cross-workspace invisibility carried by RLS alone (this
// package's stores add no workspace_id filter of their own — the GUC
// transaction is the only gate).

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type rateEnv struct {
	owner *pgx.Conn
	store *RateStore
}

func setupRateStore(t *testing.T) *rateEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	return &rateEnv{owner: owner, store: NewRateStore(pool)}
}

// seedWorkspace inserts one throwaway workspace and returns a context
// carrying its workspace GUC — every store call in this file rides that
// GUC transaction exactly like production code, RLS included.
func (e *rateEnv) seedWorkspace(ctx context.Context, t *testing.T) (ids.UUID, context.Context) {
	t.Helper()
	ws := ids.NewV7()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'RatePricing', $2, 'EUR')`,
		ws, "rp-"+ws.String()); err != nil {
		t.Fatal(err)
	}
	return ws, principal.WithWorkspaceID(context.Background(), ws)
}

// insertRate seeds one ai_model_rate row directly on the owner
// connection — this task builds no insert path of its own (that is a
// later task's job, per the brief), so fixtures write the row the same
// way any other seed fixture in this repo writes tenant data it doesn't
// own an insert helper for yet.
func (e *rateEnv) insertRate(ctx context.Context, t *testing.T, ws ids.UUID, r ModelRate) {
	t.Helper()
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO ai_model_rate (workspace_id, provider, model_id, input_per_mtok_microusd,
		  output_per_mtok_microusd, cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		ws, r.Provider, r.ModelID, r.InputPerMTokMicroUSD, r.OutputPerMTokMicroUSD,
		r.CacheReadPerMTokMicroUSD, r.CacheWritePerMTokMicroUSD, r.EffectiveDate); err != nil {
		t.Fatalf("insert rate %+v: %v", r, err)
	}
}

// callFixture is one ai_call row's pricing-relevant columns; every other
// column (latency, kind, …) takes its schema default.
type callFixture struct {
	task                                                Task
	tier                                                Tier
	provider, model                                     string
	tokensIn, tokensOut, cachedTokens, cacheWriteTokens int
	cacheHit                                            bool
	occurredAt                                          time.Time
}

func (e *rateEnv) insertCall(ctx context.Context, t *testing.T, ws ids.UUID, c callFixture) {
	t.Helper()
	// logical_call_id has carried NOT NULL since 0100 (one row per attempt,
	// grouped by logical call) with no schema default — a fixture that
	// wants a single-attempt logical call mints its own id, the same as a
	// pre-0100 row backfilled to logical_call_id = id.
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO ai_call (workspace_id, task, tier, provider, model_id, request_fingerprint,
		  tokens_in, tokens_out, cached_tokens, cache_write_tokens, cache_hit, occurred_at, logical_call_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		ws, string(c.task), string(c.tier), c.provider, c.model, "fp-"+ids.NewV7().String(),
		c.tokensIn, c.tokensOut, c.cachedTokens, c.cacheWriteTokens, c.cacheHit, c.occurredAt, ids.NewV7()); err != nil {
		t.Fatalf("insert call %+v: %v", c, err)
	}
}

func TestRateForResolvesEffectiveDateAcrossARateChange(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	ws, wsCtx := e.seedWorkspace(ctx, t)

	jan1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jun1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	e.insertRate(ctx, t, ws, ModelRate{
		Provider: providerAnthropic, ModelID: "claude-test-model",
		InputPerMTokMicroUSD: 1_000_000, OutputPerMTokMicroUSD: 1_000_000, EffectiveDate: jan1,
	})
	e.insertRate(ctx, t, ws, ModelRate{
		Provider: providerAnthropic, ModelID: "claude-test-model",
		InputPerMTokMicroUSD: 2_000_000, OutputPerMTokMicroUSD: 2_000_000, EffectiveDate: jun1,
	})

	t.Run("before either rate exists → unpriced", func(t *testing.T) {
		got, err := e.store.RateFor(wsCtx, providerAnthropic, "claude-test-model", time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("got %+v, want nil (no rate effective yet)", got)
		}
	})

	t.Run("between the two rates → the first one still applies", func(t *testing.T) {
		got, err := e.store.RateFor(wsCtx, providerAnthropic, "claude-test-model", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.InputPerMTokMicroUSD != 1_000_000 {
			t.Fatalf("got %+v, want the jan1 rate (1_000_000)", got)
		}
	})

	t.Run("on or after the rate change → the new rate applies", func(t *testing.T) {
		got, err := e.store.RateFor(wsCtx, providerAnthropic, "claude-test-model", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.InputPerMTokMicroUSD != 2_000_000 {
			t.Fatalf("got %+v, want the jun1 rate (2_000_000)", got)
		}
	})

	t.Run("unknown model → unpriced, never an error", func(t *testing.T) {
		got, err := e.store.RateFor(wsCtx, providerAnthropic, "no-such-model", jun1)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})
}

// costReportFixture is the pricing shape TestCostReportPricesTheWindowAndCountsUnpriced
// exercises: one rate and a window's worth of ai_call rows spanning every
// free/priced/unpriced/out-of-window case the report must distinguish.
type costReportFixture struct {
	rate                                  ModelRate
	from, to                              time.Time
	priced, unpriced, cacheHit, zeroUsage callFixture
	pricedNoCache, outsideWindow          callFixture
}

// buildCostReportFixture constructs the fixture set: a Feb-2026 window,
// one rate for "claude-test-model", and the six calls
// TestCostReportPricesTheWindowAndCountsUnpriced asserts over.
func buildCostReportFixture() costReportFixture {
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	inWindow := from.Add(24 * time.Hour)
	return costReportFixture{
		rate: ModelRate{
			Provider: providerAnthropic, ModelID: "claude-test-model",
			InputPerMTokMicroUSD: 5_000_000, OutputPerMTokMicroUSD: 25_000_000,
			CacheReadPerMTokMicroUSD: 500_000, CacheWritePerMTokMicroUSD: 6_250_000,
			EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		from: from, to: to,
		// priced: rate matches, real usage, no cache hit.
		priced: callFixture{
			task: TaskSummarize, provider: providerAnthropic, model: "claude-test-model",
			tokensIn: 700, cachedTokens: 400, cacheWriteTokens: 200, tokensOut: 50,
			occurredAt: inWindow,
		},
		// unpriced: same task, no rate row for this model.
		unpriced: callFixture{
			task: TaskSummarize, provider: providerAnthropic, model: "no-rate-model",
			tokensIn: 100, tokensOut: 50, occurredAt: inWindow.Add(time.Hour),
		},
		// free by construction: cache_hit — must not count as unpriced
		// despite having no rate lookup performed for it either.
		cacheHit: callFixture{
			task: TaskSummarize, provider: providerAnthropic, model: "no-rate-model",
			tokensIn: 700, tokensOut: 50, cacheHit: true, occurredAt: inWindow.Add(2 * time.Hour),
		},
		// free by construction: zero provider usage (failed before the call).
		zeroUsage: callFixture{
			task: TaskSummarize, provider: providerAnthropic, model: "no-rate-model",
			occurredAt: inWindow.Add(3 * time.Hour),
		},
		// a second, uncached priced row under a different task.
		pricedNoCache: callFixture{
			task: TaskEnrich, provider: providerAnthropic, model: "claude-test-model",
			tokensIn: 200, tokensOut: 20, occurredAt: inWindow.Add(4 * time.Hour),
		},
		// outside the window: must not be counted at all.
		outsideWindow: callFixture{
			task: TaskSummarize, provider: providerAnthropic, model: "claude-test-model",
			tokensIn: 700, cachedTokens: 400, cacheWriteTokens: 200, tokensOut: 50,
			occurredAt: to.Add(time.Hour),
		},
	}
}

func TestCostReportPricesTheWindowAndCountsUnpriced(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	ws, wsCtx := e.seedWorkspace(ctx, t)

	f := buildCostReportFixture()
	e.insertRate(ctx, t, ws, f.rate)
	for _, c := range []callFixture{f.priced, f.unpriced, f.cacheHit, f.zeroUsage, f.pricedNoCache, f.outsideWindow} {
		e.insertCall(ctx, t, ws, c)
	}

	report, err := e.store.CostReport(wsCtx, f.from, f.to)
	if err != nil {
		t.Fatal(err)
	}
	// Every fixture call lands on the same calendar day (inWindow ± a few
	// hours), so CostReport's (day, task) grouping collapses to one line
	// per task here — the day dimension is proven separately by
	// TestCostReportGroupsByCalendarDay below.
	byTask := make(map[Task]DayCost, len(report))
	for _, dc := range report {
		byTask[dc.Task] = dc
	}

	// Row-by-row cross-check: the aggregate SQL's arithmetic must equal
	// summing PriceCall over exactly the priced (non-free) rows, per the
	// same rate.
	wantSummarizeCost := PriceCall(Usage{
		TokensIn: f.priced.tokensIn, CachedTokens: f.priced.cachedTokens,
		CacheWriteTokens: f.priced.cacheWriteTokens, TokensOut: f.priced.tokensOut,
	}, f.rate)
	wantEnrichCost := PriceCall(Usage{TokensIn: f.pricedNoCache.tokensIn, TokensOut: f.pricedNoCache.tokensOut}, f.rate)

	summarize, ok := byTask[TaskSummarize]
	if !ok {
		t.Fatal("no DayCost line for summarize")
	}
	if summarize.CostMicroUSD != wantSummarizeCost {
		t.Errorf("summarize cost = %d, want %d (PriceCall cross-check)", summarize.CostMicroUSD, wantSummarizeCost)
	}
	if summarize.UnpricedCalls != 1 {
		t.Errorf("summarize unpriced_calls = %d, want 1 (only the no-rate-model, real-usage, non-cache-hit row)", summarize.UnpricedCalls)
	}
	if summarize.Day.Format(time.DateOnly) != f.priced.occurredAt.Format(time.DateOnly) {
		t.Errorf("summarize day = %s, want %s", summarize.Day.Format(time.DateOnly), f.priced.occurredAt.Format(time.DateOnly))
	}

	enrich, ok := byTask[TaskEnrich]
	if !ok {
		t.Fatal("no DayCost line for enrich")
	}
	if enrich.CostMicroUSD != wantEnrichCost {
		t.Errorf("enrich cost = %d, want %d (PriceCall cross-check)", enrich.CostMicroUSD, wantEnrichCost)
	}
	if enrich.UnpricedCalls != 0 {
		t.Errorf("enrich unpriced_calls = %d, want 0", enrich.UnpricedCalls)
	}
}

// TestCostReportGroupsByCalendarDay proves the grouping's other
// dimension: two calls for the same task and rate on different days
// price into two separate DayCost lines, never one window total — the
// grain AIRT-WIRE-1's /ai/usage merge (usage.go) depends on to attach
// cost onto the right day's task row.
func TestCostReportGroupsByCalendarDay(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	ws, wsCtx := e.seedWorkspace(ctx, t)

	rate := ModelRate{
		Provider: providerAnthropic, ModelID: "claude-test-model",
		InputPerMTokMicroUSD: 5_000_000, OutputPerMTokMicroUSD: 25_000_000,
		EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	e.insertRate(ctx, t, ws, rate)

	day1 := time.Date(2026, 2, 5, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 2, 6, 10, 0, 0, 0, time.UTC)
	e.insertCall(ctx, t, ws, callFixture{
		task: TaskSummarize, provider: providerAnthropic, model: "claude-test-model",
		tokensIn: 100, tokensOut: 10, occurredAt: day1,
	})
	e.insertCall(ctx, t, ws, callFixture{
		task: TaskSummarize, provider: providerAnthropic, model: "claude-test-model",
		tokensIn: 300, tokensOut: 30, occurredAt: day2,
	})

	report, err := e.store.CostReport(wsCtx, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 2 {
		t.Fatalf("report has %d lines, want 2 (one per day)", len(report))
	}
	byDay := make(map[string]DayCost, len(report))
	for _, dc := range report {
		byDay[dc.Day.Format(time.DateOnly)] = dc
	}
	want1 := PriceCall(Usage{TokensIn: 100, TokensOut: 10}, rate)
	want2 := PriceCall(Usage{TokensIn: 300, TokensOut: 30}, rate)
	if got, ok := byDay["2026-02-05"]; !ok || got.CostMicroUSD != want1 {
		t.Errorf("day1 = %+v, want cost %d", got, want1)
	}
	if got, ok := byDay["2026-02-06"]; !ok || got.CostMicroUSD != want2 {
		t.Errorf("day2 = %+v, want cost %d", got, want2)
	}
}

// TestCostReportGroupsByTierNotJustTask proves the double-counting fix
// (AIRT-WIRE-1's wire rows are per day × task × TIER, and a client sums
// cost_est_minor across rows): when the same task runs on two tiers the
// same day, CostReport must return one line PER TIER — each priced only
// from its own tier's calls — rather than one task-day total that a
// caller would broadcast onto both tier rows and double-count.
func TestCostReportGroupsByTierNotJustTask(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	ws, wsCtx := e.seedWorkspace(ctx, t)

	rate := ModelRate{
		Provider: providerAnthropic, ModelID: "claude-test-model",
		InputPerMTokMicroUSD: 5_000_000, OutputPerMTokMicroUSD: 25_000_000,
		EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	e.insertRate(ctx, t, ws, rate)

	day := time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)
	cheapCall := callFixture{
		task: TaskSummarize, tier: TierCheapCloud, provider: providerAnthropic, model: "claude-test-model",
		tokensIn: 100, tokensOut: 10, occurredAt: day,
	}
	premiumCall := callFixture{
		task: TaskSummarize, tier: TierPremium, provider: providerAnthropic, model: "claude-test-model",
		tokensIn: 900, tokensOut: 90, occurredAt: day.Add(time.Hour),
	}
	e.insertCall(ctx, t, ws, cheapCall)
	e.insertCall(ctx, t, ws, premiumCall)

	report, err := e.store.CostReport(wsCtx, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	var summarizeLines []DayCost
	for _, dc := range report {
		if dc.Task == TaskSummarize {
			summarizeLines = append(summarizeLines, dc)
		}
	}
	if len(summarizeLines) != 2 {
		t.Fatalf("got %d summarize lines, want 2 (one per tier) — %+v", len(summarizeLines), summarizeLines)
	}

	byTier := make(map[Tier]DayCost, len(summarizeLines))
	for _, dc := range summarizeLines {
		byTier[dc.Tier] = dc
	}
	wantCheap := PriceCall(Usage{TokensIn: cheapCall.tokensIn, TokensOut: cheapCall.tokensOut}, rate)
	wantPremium := PriceCall(Usage{TokensIn: premiumCall.tokensIn, TokensOut: premiumCall.tokensOut}, rate)

	cheap, ok := byTier[TierCheapCloud]
	if !ok || cheap.CostMicroUSD != wantCheap {
		t.Fatalf("cheap_cloud line = %+v, want cost %d", cheap, wantCheap)
	}
	premium, ok := byTier[TierPremium]
	if !ok || premium.CostMicroUSD != wantPremium {
		t.Fatalf("premium line = %+v, want cost %d", premium, wantPremium)
	}

	// The whole point of the fix: summing the two tier rows equals the
	// true task-day total — no duplication from broadcasting one row's
	// cost onto the other.
	wantTotal := wantCheap + wantPremium
	if got := cheap.CostMicroUSD + premium.CostMicroUSD; got != wantTotal {
		t.Fatalf("summed tier costs = %d, want %d (the true task-day total, no double-counting)", got, wantTotal)
	}
}

// TestCostReportPricesEmbeddingCallsHonestly is the integration proof for
// the I1 fix (router.go's Embed now stamps the meter's token estimate
// onto the trace row instead of leaving it 0): a genuinely paid
// embedding call — tokens_in > 0, tokens_out = 0, exactly the shape
// router.Embed traces post-fix — must price against its rate like any
// other call, never fall into the free-by-construction exemption (that
// exemption is reserved for tokens_in = 0 AND tokens_out = 0, the "never
// reached the provider" case). It also proves the honest-0 side: a local
// embed model's seeded all-zero rate prices to 0 AND does not count as
// unpriced (a rate row exists — it is just $0), while a genuinely unrated
// model still counts unpriced. Rates come from the real SeedModelRates
// sheet, not hand-rolled fixture rates, so this also proves the seed rows
// this fix added (gemini-embedding-001, ollama/bge-m3) actually resolve.
func TestCostReportPricesEmbeddingCallsHonestly(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	ws, wsCtx := e.seedWorkspace(ctx, t)

	effective := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	for _, r := range SeedModelRates(effective) {
		e.insertRate(ctx, t, ws, r)
	}

	day := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	// Paid embedding call: gemini-embedding-001 has a seeded nonzero input
	// rate. tokens_out stays 0 — embeddings have no output.
	e.insertCall(ctx, t, ws, callFixture{
		task: TaskEmbeddings, tier: TierEmbedLane, provider: providerGemini, model: "gemini-embedding-001",
		tokensIn: 10_000, occurredAt: day,
	})
	// Local/offline embedding call: bge-m3 has a seeded ALL-ZERO rate — it
	// must price to 0 but must NOT count as unpriced (a rate row exists).
	e.insertCall(ctx, t, ws, callFixture{
		task: TaskEmbeddings, tier: TierEmbedLane, provider: providerOllama, model: "bge-m3",
		tokensIn: 10_000, occurredAt: day.Add(time.Hour),
	})
	// Unrated embedding call: no seed row for this model at all — must
	// come back unpriced, never a silent 0.
	e.insertCall(ctx, t, ws, callFixture{
		task: TaskEmbeddings, tier: TierEmbedLane, provider: providerGemini, model: "gemini-embedding-999-unreleased",
		tokensIn: 10_000, occurredAt: day.Add(2 * time.Hour),
	})

	report, err := e.store.CostReport(wsCtx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 {
		t.Fatalf("report has %d lines, want 1 (all three calls share day+task+tier): %+v", len(report), report)
	}
	line := report[0]
	if line.Task != TaskEmbeddings || line.Tier != TierEmbedLane {
		t.Fatalf("unexpected report line: %+v", line)
	}

	wantGeminiCost := PriceCall(Usage{TokensIn: 10_000}, ModelRate{InputPerMTokMicroUSD: 150_000})
	if wantGeminiCost <= 0 {
		t.Fatal("test fixture bug: expected gemini-embedding-001's priced cost to be > 0")
	}
	if line.CostMicroUSD != wantGeminiCost {
		t.Errorf("cost_microusd = %d, want %d (gemini-embedding-001's real rate — before the I1 fix this read 0, a silent misleading free)", line.CostMicroUSD, wantGeminiCost)
	}
	if line.UnpricedCalls != 1 {
		t.Errorf("unpriced_calls = %d, want 1 (only the model with no seed rate at all — bge-m3's zero rate is a REAL price, not unpriced)", line.UnpricedCalls)
	}
}

func TestRateAndCallVisibilityIsScopedByWorkspaceRLS(t *testing.T) {
	e := setupRateStore(t)
	ctx := context.Background()
	_, ctxA := e.seedWorkspace(ctx, t)
	wsB, ctxB := e.seedWorkspace(ctx, t)

	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.insertRate(ctx, t, wsB, ModelRate{
		Provider: providerAnthropic, ModelID: "shared-model-name",
		InputPerMTokMicroUSD: 9_000_000, OutputPerMTokMicroUSD: 9_000_000, EffectiveDate: day,
	})
	e.insertCall(ctx, t, wsB, callFixture{
		task: TaskSummarize, provider: providerAnthropic, model: "shared-model-name",
		tokensIn: 100, tokensOut: 50, occurredAt: day.Add(24 * time.Hour),
	})

	// Workspace A's context must never see workspace B's rate row — same
	// provider/model, RLS is the only thing standing between them.
	got, err := e.store.RateFor(ctxA, providerAnthropic, "shared-model-name", day.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("workspace A saw workspace B's rate: %+v", got)
	}

	// And workspace A's CostReport over the same window must not pick up
	// workspace B's call.
	report, err := e.store.CostReport(ctxA, day, day.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range report {
		if tc.Task == TaskSummarize {
			t.Fatalf("workspace A's cost report saw workspace B's call: %+v", tc)
		}
	}

	// Sanity: workspace B's own context DOES see it (proves the isolation
	// above is RLS working, not a fixture that silently inserted nothing).
	gotB, err := e.store.RateFor(ctxB, providerAnthropic, "shared-model-name", day.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if gotB == nil {
		t.Fatal("workspace B could not see its own rate row")
	}
	reportB, err := e.store.CostReport(ctxB, day, day.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tc := range reportB {
		if tc.Task == TaskSummarize {
			found = true
		}
	}
	if !found {
		t.Fatal("workspace B's own cost report did not see its own call")
	}
}
