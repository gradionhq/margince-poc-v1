// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package ai

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestRunTransparencyReadsAndPricesOneCorrelatedProductRun(t *testing.T) {
	env := setupRateStore(t)
	background := context.Background()
	workspaceID, workspaceCtx := env.seedWorkspace(background, t)
	correlationID := ids.NewV7()
	usedAt := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	env.insertRate(background, t, workspaceID, ModelRate{
		Provider: providerAnthropic, ModelID: "claude-run-model",
		InputPerMTokMicroUSD: 1_000_000, OutputPerMTokMicroUSD: 5_000_000,
		CacheReadPerMTokMicroUSD: 500_000, CacheWritePerMTokMicroUSD: 2_000_000,
		EffectiveDate: usedAt.AddDate(0, 0, -1),
	})
	if _, err := env.owner.Exec(background, `
		INSERT INTO ai_call (workspace_id, correlation_id, task, tier, provider, model_id,
		  served_model, request_fingerprint, tokens_in, tokens_out, reasoning_tokens,
		  cached_tokens, cache_write_tokens, latency_ms, occurred_at, logical_call_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		workspaceID, correlationID, string(TaskColdStart), string(TierCheapCloud), providerAnthropic,
		"claude-run-model", "claude-run-model-202607", "run-fingerprint", 1_000, 100, 20,
		200, 50, 750, usedAt, ids.NewV7()); err != nil {
		t.Fatal(err)
	}

	readCtx := principal.WithActor(workspaceCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"}, RowScope: principal.RowScopeAll,
			Objects: map[string]principal.ObjectGrant{"organization": {Read: true}},
		},
	})
	summary, err := NewRunTransparency(env.store.pool).Get(readCtx, correlationID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CallAttempts != 1 || summary.TokensIn != 1_000 || summary.TokensOut != 100 ||
		summary.LatencyMS != 750 || summary.UnpricedCalls != 0 || len(summary.Models) != 1 {
		t.Fatalf("run summary = %+v", summary)
	}
	if summary.Models[0].ConfiguredModel != "claude-run-model" ||
		summary.Models[0].ServedModel != "claude-run-model-202607" || summary.EstimatedCostMicroUSD == 0 {
		t.Fatalf("model/cost transparency = %+v", summary)
	}

	createCtx := principal.WithActor(workspaceCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"installer"}, RowScope: principal.RowScopeAll,
			Objects: map[string]principal.ObjectGrant{"organization": {Create: true}},
		},
	})
	if _, err := NewRunTransparency(env.store.pool).Get(createCtx, correlationID); err != nil {
		t.Fatalf("installer create fallback: %v", err)
	}
	if _, err := NewRunTransparency(env.store.pool).Get(workspaceCtx, correlationID); err == nil {
		t.Fatal("an unauthenticated caller read correlated model telemetry")
	}
}
