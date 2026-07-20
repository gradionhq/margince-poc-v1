// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// GET /ai/usage over the real wire (AIRT-WIRE-1): the day × task × tier
// report plus the budget band for the caller's workspace, and the window
// refusals — an inverted or over-wide range is a typed 422 before the
// unpaginated aggregation runs.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedAiUsage writes the AIRT-PARAM-33 meter rows the report aggregates:
// two days, one with two tiers of the same task — through the owner
// connection under the workspace GUC, exactly as the meter's upsert lands
// them.
func seedAiUsage(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ai_usage (workspace_id, day, task, tier, calls, cached_hits, tokens_in, tokens_out) VALUES
		($1, '2026-07-10', 'capture_classify', 'local_small', 4, 1, 1200, 300),
		($1, '2026-07-10', 'capture_classify', 'cheap_cloud', 1, 0, 800, 200),
		($1, '2026-07-11', 'enrich', 'cheap_cloud', 2, 0, 500, 120)`, wsID); err != nil {
		t.Fatalf("seeding ai_usage: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

type aiUsageDTO struct {
	Days []struct {
		Date  string `json:"date"`
		Tasks []struct {
			Task         string `json:"task"`
			Tier         string `json:"tier"`
			Calls        int    `json:"calls"`
			TokensIn     int    `json:"tokens_in"`
			TokensOut    int    `json:"tokens_out"`
			CostEstMinor *int   `json:"cost_est_minor"`
		} `json:"tasks"`
	} `json:"days"`
	Budget struct {
		MonthlyTokens int     `json:"monthly_tokens"`
		SpentTokens   int     `json:"spent_tokens"`
		Band          string  `json:"band"`
		Currency      *string `json:"currency"`
	} `json:"budget"`
}

func TestAiUsageOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A fresh workspace: no spend yet, but the report and its budget
	// block still answer — the spend is never invisible, even at zero.
	var usage aiUsageDTO
	if status := e.call(t, "GET", "/v1/ai/usage", nil, nil, &usage); status != http.StatusOK {
		t.Fatalf("GET /ai/usage → %d, want 200", status)
	}
	if usage.Budget.Band == "" {
		t.Fatal("usage report carries no budget band")
	}
	if len(usage.Days) != 0 {
		t.Fatalf("a workspace with no AI calls reports %d days, want 0", len(usage.Days))
	}

	// Two metered days: the report groups day × task × tier as recorded.
	seedAiUsage(t, e)
	if status := e.call(t, "GET", "/v1/ai/usage?from=2026-07-01&to=2026-07-14", nil, nil, &usage); status != http.StatusOK {
		t.Fatalf("windowed GET /ai/usage → %d, want 200", status)
	}
	if len(usage.Days) != 2 {
		t.Fatalf("report groups %d days, want 2", len(usage.Days))
	}
	first := usage.Days[0]
	if len(first.Tasks) != 2 {
		t.Fatalf("day one carries %d task lines, want 2 (two tiers)", len(first.Tasks))
	}
	if first.Tasks[0].Calls == 0 || first.Tasks[0].TokensIn == 0 {
		t.Fatalf("task line %+v reports zero spend for a metered day", first.Tasks[0])
	}
	if usage.Budget.SpentTokens == 0 {
		t.Fatal("budget block reports zero spend after metered calls")
	}

	// The refusals, before the aggregation runs: inverted, then over-wide.
	if status := e.call(t, "GET", "/v1/ai/usage?from=2026-07-14&to=2026-07-01", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("inverted window → %d, want 422", status)
	}
	if status := e.call(t, "GET", "/v1/ai/usage?from=2020-01-01&to=2026-07-14", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("over-wide window → %d, want 422 (366-day cap)", status)
	}
}

// seedAiCall inserts one ai_call trace row directly on the owner
// connection — the same real-Postgres shortcut ratestore_integration_test.go
// uses (this suite builds no insert path of its own for it either).
// provider/model are caller-chosen so a test can point one call at a
// rated model and another at a model no ai_model_rate row covers.
func seedAiCall(t *testing.T, e *env, wsID string, task, provider, model string, tokensIn, tokensOut int, occurredAt time.Time) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO ai_call (workspace_id, task, provider, model_id, request_fingerprint,
		  tokens_in, tokens_out, occurred_at, logical_call_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		wsID, task, provider, model, "fp-"+ids.NewV7().String(),
		tokensIn, tokensOut, occurredAt, ids.NewV7()); err != nil {
		t.Fatalf("insert ai_call: %v", err)
	}
}

// seedAiModelRate inserts one ai_model_rate row effective on day.
func seedAiModelRate(t *testing.T, e *env, wsID string, day time.Time) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO ai_model_rate (workspace_id, provider, model_id, input_per_mtok_microusd,
		  output_per_mtok_microusd, cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date)
		VALUES ($1,'anthropic','claude-test-model',5000000,25000000,500000,6250000,$2)`,
		wsID, day); err != nil {
		t.Fatalf("insert ai_model_rate: %v", err)
	}
}

// TestAiUsageCostOverHTTP proves AIRT-WIRE-1's cost merge (ADR-0067,
// price-on-read) end to end over the real wire, on top of seedAiUsage's
// fixture (2026-07-10 capture_classify across two tiers, 2026-07-11
// enrich): a rated day/task reports a hand-computable, non-zero
// cost_est_minor on EVERY tier line of that task (CostReport groups by
// day + task, one grain coarser than the wire's day × task × tier —
// usage.go attaches the shared task-day total to each tier row rather
// than fabricate a per-tier split) and currency USD; a day/task with no
// matching rate omits cost_est_minor instead of reporting a fabricated 0.
func TestAiUsageCostOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	seedAiUsage(t, e)
	ctx := context.Background()
	var wsID string
	if err := e.owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}

	// capture_classify/2026-07-10: rated. 2_000 in / 500 out at the
	// seeded sheet price (5_000_000/25_000_000 microUSD per MTok) =
	// (2_000*5_000_000 + 500*25_000_000)/1_000_000 = 22_500 microUSD =
	// 2 cents (truncating) — a provably non-zero, hand-computable minor
	// amount.
	seedAiModelRate(t, e, wsID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	seedAiCall(t, e, wsID, "capture_classify", "anthropic", "claude-test-model", 2_000, 500, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	// enrich/2026-07-11: seedAiUsage already counts calls for this
	// task/day, but "no-rate-model" has no ai_model_rate row (neither the
	// workspace-default seed sheet nor this test's own seedAiModelRate
	// names it) — must come back unpriced, never a silent 0.
	seedAiCall(t, e, wsID, "enrich", "anthropic", "no-rate-model", 500, 100, time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC))

	var usage aiUsageDTO
	if status := e.call(t, "GET", "/v1/ai/usage?from=2026-07-01&to=2026-07-31", nil, nil, &usage); status != http.StatusOK {
		t.Fatalf("GET /ai/usage → %d, want 200", status)
	}
	if usage.Budget.Currency == nil || *usage.Budget.Currency != "USD" {
		t.Fatalf("budget.currency = %v, want USD", usage.Budget.Currency)
	}

	var captureClassifyCosts []int
	var enrichCost *int
	var sawEnrich bool
	for _, day := range usage.Days {
		for _, task := range day.Tasks {
			switch task.Task {
			case "capture_classify":
				if task.CostEstMinor == nil {
					t.Fatalf("capture_classify/%s tier %s cost_est_minor is nil, want the resolved 2-cent cost", day.Date, task.Tier)
				}
				captureClassifyCosts = append(captureClassifyCosts, *task.CostEstMinor)
			case "enrich":
				sawEnrich = true
				enrichCost = task.CostEstMinor
			}
		}
	}
	if len(captureClassifyCosts) != 2 {
		t.Fatalf("capture_classify has %d tier lines, want 2 (seedAiUsage's local_small + cheap_cloud)", len(captureClassifyCosts))
	}
	for _, got := range captureClassifyCosts {
		if got != 2 {
			t.Errorf("capture_classify tier line cost_est_minor = %d, want 2 (22_500 microUSD / 10_000, shared across both tier rows)", got)
		}
	}
	if !sawEnrich {
		t.Fatal("enrich task line is missing from the report entirely")
	}
	if enrichCost != nil {
		t.Fatalf("enrich cost_est_minor = %d, want omitted (nil) — no rate row exists for it", *enrichCost)
	}
}
