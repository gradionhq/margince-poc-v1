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
			Task      string `json:"task"`
			Tier      string `json:"tier"`
			Calls     int    `json:"calls"`
			TokensIn  int    `json:"tokens_in"`
			TokensOut int    `json:"tokens_out"`
		} `json:"tasks"`
	} `json:"days"`
	Budget struct {
		MonthlyTokens int    `json:"monthly_tokens"`
		SpentTokens   int    `json:"spent_tokens"`
		Band          string `json:"band"`
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
