// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// buildRecord's own tests: per-bucket token means, ADR-0067 pricing against
// the cert lane's in-memory seed rate sheet, and the byte-stable
// determinism record.go's own doc promises. Split out of runner_test.go
// (which covers the router-driving pipeline runner.go itself owns) because
// buildRecord/seedRateFor/percentile now live in record.go alongside the
// Record type they build — same split rationale, mirrored on the test side.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// withFixedNow overrides nowFunc for the duration of one test and restores
// it on cleanup — the same seam Run's own MARGINCE_AICERT_TRACE filename
// stamp uses, borrowed here so a buildRecord test can pin RanAt (and the
// pricing snapshot date derived from it) instead of racing the wall clock.
func withFixedNow(t *testing.T, at time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return at }
	t.Cleanup(func() { nowFunc = prev })
}

// TestBuildRecordPricesPerBucketMeansAgainstTheSeedRateSheet pins the
// hand-computed ADR-0067 price for a known two-run pooled total against
// anthropic:claude-haiku-4-5-20251001's seeded rate (in=1_000_000,
// out=5_000_000, cache_read=100_000, cache_write=1_250_000 microUSD/MTok):
//
//	totals:  tokens_in=3000 tokens_out=500 cached=400 cache_write=200 (n=2)
//	means:   in=1500 out=250 cached=200 cache_write=100
//	uncached = 1500 - 200 - 100 = 1200
//	microUSD·tokens = 1200*1_000_000 + 200*100_000 + 100*1_250_000 + 250*5_000_000
//	               = 1_200_000_000 + 20_000_000 + 125_000_000 + 1_250_000_000
//	               = 2_595_000_000
//	est_cost_microusd = 2_595_000_000 / 1_000_000 = 2595
func TestBuildRecordPricesPerBucketMeansAgainstTheSeedRateSheet(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))

	results := []RunResult{{Score: 80, HardPass: true}, {Score: 90, HardPass: true}}
	latencies := []int64{100, 200}
	rec := buildRecord(ai.TaskSummarize, VerdictCertified, 1, results, latencies,
		3000, 500, 400, 200,
		"anthropic", "claude-haiku-4-5-20251001", "response", "claude-opus-4-8", false,
		ai.RoutingConfig{Profile: ai.ProfileEUHosted})

	if rec.MeanTokensIn != 1500 || rec.MeanTokensOut != 250 || rec.MeanCachedTokens != 200 || rec.MeanCacheWriteTokens != 100 {
		t.Fatalf("mean buckets = in=%d out=%d cached=%d cache_write=%d, want 1500/250/200/100",
			rec.MeanTokensIn, rec.MeanTokensOut, rec.MeanCachedTokens, rec.MeanCacheWriteTokens)
	}
	if rec.MeanTokens != 1750 {
		t.Fatalf("mean_tokens = %d, want 1750 (the exact (3000+500)/2, unaffected by the per-bucket split)", rec.MeanTokens)
	}
	if rec.EstCostMicroUSD != 2595 {
		t.Fatalf("est_cost_microusd = %d, want 2595", rec.EstCostMicroUSD)
	}
}

// TestBuildRecordUnpricedWhenNoSeedRateMatchesTheServedModel proves the
// price-on-read honesty rule: a served model with no exact (provider,
// model) row in ai.SeedModelRates leaves EstCostMicroUSD at an honest 0,
// never a fabricated price extrapolated from a near-miss.
func TestBuildRecordUnpricedWhenNoSeedRateMatchesTheServedModel(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))

	results := []RunResult{{Score: 80, HardPass: true}}
	rec := buildRecord(ai.TaskSummarize, VerdictCertified, 1, results, []int64{100},
		1000, 200, 0, 0,
		"anthropic", "claude-does-not-exist", "response", "claude-opus-4-8", false,
		ai.RoutingConfig{Profile: ai.ProfileEUHosted})

	if rec.EstCostMicroUSD != 0 {
		t.Fatalf("est_cost_microusd = %d, want 0 for an unrated served model", rec.EstCostMicroUSD)
	}
	if rec.MeanTokensIn != 1000 || rec.MeanTokensOut != 200 {
		t.Fatalf("mean buckets still owed even when unpriced: in=%d out=%d, want 1000/200", rec.MeanTokensIn, rec.MeanTokensOut)
	}
}

// TestBuildRecordIsByteForByteDeterministicForIdenticalInputs proves the
// aicert determinism contract (record.go's own doc: "the same []RunResult
// always produces the same Record byte-for-byte except for whatever the
// caller puts in RanAt") still holds now that pricing and the four new
// bucket-mean fields are in the mix: with nowFunc pinned, two buildRecord
// calls over identical inputs must marshal to identical bytes.
func TestBuildRecordIsByteForByteDeterministicForIdenticalInputs(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))

	call := func() Record {
		results := []RunResult{{Score: 80, HardPass: true}, {Score: 90, HardPass: true}}
		return buildRecord(ai.TaskSummarize, VerdictCertified, 1, results, []int64{100, 200},
			3000, 500, 400, 200,
			"anthropic", "claude-haiku-4-5-20251001", "response", "claude-opus-4-8", false,
			ai.RoutingConfig{Profile: ai.ProfileEUHosted})
	}

	first, err := json.Marshal(call())
	if err != nil {
		t.Fatalf("marshaling first record: %v", err)
	}
	second, err := json.Marshal(call())
	if err != nil {
		t.Fatalf("marshaling second record: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("two buildRecord calls over identical inputs produced different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
