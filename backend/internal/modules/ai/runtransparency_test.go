// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"testing"
	"time"
)

func TestSummarizeRunKeepsModelIdentityCostAndUnpricedUsageVisible(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	rate := ModelRate{
		InputPerMTokMicroUSD: 1_000_000, OutputPerMTokMicroUSD: 5_000_000,
	}
	summary := summarizeRun([]runCall{
		{
			Task: "site_extract", Tier: "premium", Provider: "gemini",
			ConfiguredModel: "gemini-3.5-flash", ServedModel: "gemini-3.5-flash-2026-07",
			ServedIdentitySource: servedIdentitySourceResponse,
			TokensIn:             1_000, TokensOut: 100, LatencyMS: 900, OccurredAt: now,
			RateFound: true, Rate: rate,
		},
		{
			Task: "site_extract", Tier: "premium", Provider: "gemini",
			ConfiguredModel: "gemini-3.5-flash", ServedModel: "gemini-3.5-flash-2026-07",
			ServedIdentitySource: servedIdentitySourceResponse,
			TokensIn:             500, TokensOut: 50, CachedTokens: 100, LatencyMS: 1_300,
			OccurredAt: now.Add(time.Second), IsTerminal: true, RateFound: true, Rate: rate,
		},
		{
			Task: "cold_start", Tier: "cheap_cloud", Provider: "new-provider",
			ConfiguredModel: "new-model", ServedModel: "new-model-snapshot",
			ServedIdentitySource: servedIdentitySourceResponse,
			TokensIn:             200, TokensOut: 20, LatencyMS: 200, OccurredAt: now.Add(2 * time.Second), IsTerminal: true,
		},
	})

	if summary.Currency != "USD" || summary.CallAttempts != 3 || summary.UnpricedCalls != 1 {
		t.Fatalf("summary identity = %+v", summary)
	}
	if summary.TokensIn != 1_700 || summary.TokensOut != 170 || summary.LatencyMS != 1_500 {
		t.Fatalf("summary usage = %+v", summary)
	}
	// First call: 1000 input + 500 output. Second: 400 uncached input,
	// 100 cache-read at the zero test rate, plus 250 output.
	if summary.EstimatedCostMicroUSD != 2_150 {
		t.Fatalf("estimated cost = %d, want 2150 micro-USD", summary.EstimatedCostMicroUSD)
	}
	if len(summary.Models) != 2 || summary.Models[0].ConfiguredModel != "gemini-3.5-flash" ||
		summary.Models[0].ServedModel != "gemini-3.5-flash-2026-07" || summary.Models[0].CallAttempts != 2 {
		t.Fatalf("model breakdown = %+v", summary.Models)
	}
}

func TestSummarizeRunOrdersTiedModelsAndNeverClaimsConfiguredFallbackWasServed(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	summary := summarizeRun([]runCall{
		{
			Task: "cold_start", Tier: "premium", Provider: "gemini",
			ConfiguredModel: "z-model", ServedModel: "z-model", ServedIdentitySource: servedIdentitySourceConfigured,
			OccurredAt: now, IsTerminal: true,
		},
		{
			Task: "cold_start", Tier: "cheap_cloud", Provider: "anthropic",
			ConfiguredModel: "a-model", ServedModel: "a-snapshot", ServedIdentitySource: servedIdentitySourceResponse,
			OccurredAt: now, IsTerminal: true,
		},
	})

	if len(summary.Models) != 2 || summary.Models[0].Tier != "cheap_cloud" || summary.Models[1].Tier != "premium" {
		t.Fatalf("tied model order = %+v", summary.Models)
	}
	if summary.Models[0].ServedModel != "a-snapshot" || summary.Models[1].ServedModel != "" {
		t.Fatalf("served identities = %+v", summary.Models)
	}
}
