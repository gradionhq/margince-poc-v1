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
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
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

	acc := ratedAccumulation()
	rec := buildRecord(ai.TaskSummarize, VerdictCertified, acc, ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")

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

	acc := ratedAccumulation()
	acc.allResults = []RunResult{{Score: 80, HardPass: true}}
	acc.latencies = []int64{100}
	acc.passed = 1
	acc.tokensInTotal, acc.tokensOutTotal, acc.cachedTokensTotal, acc.cacheWriteTokensTotal = 1000, 200, 0, 0
	acc.servedModel = "claude-does-not-exist"
	rec := buildRecord(ai.TaskSummarize, VerdictCertified, acc, ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")

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
		return buildRecord(ai.TaskSummarize, VerdictCertified, ratedAccumulation(),
			ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")
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

// TestBuildRecordCountsWhatEachRunActuallyProduced pins the record's
// per-outcome tally to the validators' own verdicts rather than to the
// pass/fail column beside them. The run set below is deliberately one a
// HardPass count could not reconstruct: the wrong answer and the invalid
// reply both failed, the abstention passed a scenario that expected it, and
// no arithmetic over HardPass alone tells those three apart.
func TestBuildRecordCountsWhatEachRunActuallyProduced(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))

	acc := ratedAccumulation()
	acc.allResults = []RunResult{
		{Score: 90, HardPass: true, Outcome: aitasks.OutcomeAccepted},
		{Score: 85, HardPass: true, Outcome: aitasks.OutcomeAccepted},
		{Score: 40, Outcome: aitasks.OutcomeWrongAnswer},
		{Score: 10, Outcome: aitasks.OutcomeInvalid},
		{Score: 70, HardPass: true, Outcome: aitasks.OutcomeAbstained},
	}
	acc.latencies = []int64{100, 100, 100, 100, 100}
	acc.passed = 3
	acc.certifiedScope = aitasks.ScopeSingleTurn
	rec := buildRecord(ai.TaskSummarize, VerdictSupportedDegraded, acc, ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")

	if rec.ReportedAccepted != 2 || rec.ReportedWrongAnswer != 1 || rec.ReportedInvalid != 1 || rec.ReportedAbstained != 1 {
		t.Fatalf("reported outcome counts = accepted=%d wrong_answer=%d invalid=%d abstained=%d, want 2/1/1/1",
			rec.ReportedAccepted, rec.ReportedWrongAnswer, rec.ReportedInvalid, rec.ReportedAbstained)
	}
	if rec.ReportedAccepted+rec.ReportedWrongAnswer+rec.ReportedInvalid+rec.ReportedAbstained != rec.Runs {
		t.Fatalf("the four counts sum to %d but the record reports %d runs — every run produced exactly one outcome",
			rec.ReportedAccepted+rec.ReportedWrongAnswer+rec.ReportedInvalid+rec.ReportedAbstained, rec.Runs)
	}
	// The counts say what came back; Passed says whether it was what was asked
	// for. Two accepted replies and an abstention passed here, so a reader
	// cannot infer either number from the other.
	if rec.Passed != 3 || rec.Reliability != 0.6 {
		t.Fatalf("passed=%d reliability=%v, want 3 and 0.6", rec.Passed, rec.Reliability)
	}
	if rec.CertifiedScope != aitasks.ScopeSingleTurn {
		t.Fatalf("certified_scope = %q, want %q — the scope is the caller's claim about its sites, not a constant",
			rec.CertifiedScope, aitasks.ScopeSingleTurn)
	}
	if rec.ContextApplied {
		t.Fatal("context_applied is true, but the cert lane runs without a database and never applies the company context prompt")
	}
}

// context_applied is one fact about the LANE: it is false on every record,
// because assembling the company context reads a database no certification run
// has. WHICH records that costs something is a fact about the task, and only the
// task's own declared scopes say it — a task production always prepends scopes
// to was certified without reference data every real call carries, and a task
// that declares none went without nothing.
//
// Derived from the contract for every task rather than pinned task by task: a
// scope added upstream must not be able to leave a record naming the old set.
func TestEveryRecordNamesTheCompanyContextItsTaskWentWithout(t *testing.T) {
	withFixedNow(t, time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	scoped := 0
	for _, task := range ai.AllTasks() {
		policy, declared := ai.CompanyContextFor(task)
		if !declared {
			t.Errorf("the contract declares no company-context policy for %s, so no record of it can be read", task)
			continue
		}
		if len(policy.Scopes) > 0 {
			scoped++
		}
		rec := buildRecord(task, VerdictCertified, ratedAccumulation(),
			ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")
		if rec.ContextApplied {
			t.Errorf("the %s record claims the company context was applied, and this lane has no database to assemble it from", task)
		}
		if !slices.Equal(rec.ContextScopes, policy.Scopes) {
			t.Errorf("the %s record names context scopes %v, and the contract has production prepend %v",
				task, rec.ContextScopes, policy.Scopes)
		}
	}
	if scoped == 0 {
		t.Fatal("no task declares a company-context scope, so every assertion above held over the empty set")
	}
}

// The contract's scope list is package state shared by every reader of it. A
// record handed the original would let anything holding one edit what the next
// record claims production prepends.
func TestARecordDoesNotHandOutTheContractsOwnScopes(t *testing.T) {
	withFixedNow(t, time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	rec := buildRecord(ai.TaskOfferDraft, VerdictCertified, ratedAccumulation(),
		ai.RoutingConfig{Profile: ai.ProfileEUHosted}, "p000000000000")
	if len(rec.ContextScopes) == 0 {
		t.Fatal("offer_draft declares no company-context scope, so this record has nothing to share")
	}

	rec.ContextScopes[0] = "edited by a record holder"

	policy, declared := ai.CompanyContextFor(ai.TaskOfferDraft)
	if !declared {
		t.Fatal("the contract declares no company-context policy for offer_draft")
	}
	if policy.Scopes[0] == rec.ContextScopes[0] {
		t.Error("editing a record's scopes edited the task contract every later record is built from")
	}
}

// ratedAccumulation is one task's folded run set, on a served model the seed
// rate sheet actually prices: two passing runs, 3000/500/400/200 pooled tokens.
// A test that changes one of those numbers copies it rather than editing here,
// so the priced arithmetic above stays pinned to the comment that derives it.
func ratedAccumulation() *taskAccumulation {
	return &taskAccumulation{
		allResults:            []RunResult{{Score: 80, HardPass: true}, {Score: 90, HardPass: true}},
		latencies:             []int64{100, 200},
		tokensInTotal:         3000,
		tokensOutTotal:        500,
		cachedTokensTotal:     400,
		cacheWriteTokensTotal: 200,
		passed:                2,
		provider:              "anthropic",
		servedModel:           "claude-haiku-4-5-20251001",
		identitySource:        "response",
		judgeServedModel:      "claude-opus-4-8",
		certifiedScope:        aitasks.ScopeFullInvocation,
	}
}
