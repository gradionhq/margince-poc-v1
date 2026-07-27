// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// One run's accounting. Every claim here is about a run that made MORE than one
// model call, because that is the only case in which "the run" and "its last
// call" can disagree — and the shipped reply drafter sends up to three.

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// A demoted attempt anywhere in the run is a demoted run: §5 voids the record
// for it, and a retry that recovers does not un-spend the budget that forced
// the demotion. Reading the last call alone certifies exactly this run.
func TestPoolRunCallsIsDegradedWhenAnySingleCallWas(t *testing.T) {
	cases := map[string][]ai.Call{
		"the first call degraded, the retry did not": {{Degraded: true}, {}},
		"only the last call degraded":                {{}, {Degraded: true}},
		"every call degraded":                        {{Degraded: true}, {Degraded: true}},
	}
	for name, calls := range cases {
		t.Run(name, func(t *testing.T) {
			pooled, err := poolRunCalls(calls)
			if err != nil {
				t.Fatalf("poolRunCalls: %v", err)
			}
			if !pooled.Degraded {
				t.Fatal("the run reads as healthy, so a demoted answer would certify")
			}
		})
	}
}

// What the run spent is what all of its calls spent. A three-call run accounted
// by its third call under-reports its own cost, and the record prices tokens.
func TestPoolRunCallsSumsWhatTheRunSpent(t *testing.T) {
	pooled, err := poolRunCalls([]ai.Call{
		{TokensIn: 100, TokensOut: 20, CachedTokens: 10, CacheWriteTokens: 5, ReasoningTokens: 8, LatencyMS: 300},
		{TokensIn: 40, TokensOut: 6, CachedTokens: 2, CacheWriteTokens: 1, ReasoningTokens: 3, LatencyMS: 120},
	})
	if err != nil {
		t.Fatalf("poolRunCalls: %v", err)
	}
	if pooled.TokensIn != 140 || pooled.TokensOut != 26 {
		t.Errorf("tokens in/out = %d/%d, want 140/26", pooled.TokensIn, pooled.TokensOut)
	}
	if pooled.CachedTokens != 12 || pooled.CacheWriteTokens != 6 || pooled.ReasoningTokens != 11 {
		t.Errorf("cached/cache_write/reasoning = %d/%d/%d, want 12/6/11",
			pooled.CachedTokens, pooled.CacheWriteTokens, pooled.ReasoningTokens)
	}
	if pooled.LatencyMS != 420 {
		t.Errorf("latency = %dms, want 420 — the run waited for both calls", pooled.LatencyMS)
	}
}

// A cap is a ceiling on the RUN. A site that answers in two calls spent both,
// and a cap charged to the last one alone passes a run that blew its budget on
// the way there.
func TestCheckCapsChargesTheWholeRun(t *testing.T) {
	pooled, err := poolRunCalls([]ai.Call{
		{TokensOut: 40, LatencyMS: 400, Provider: "anthropic"},
		{TokensOut: 40, LatencyMS: 400, Provider: "anthropic"},
	})
	if err != nil {
		t.Fatalf("poolRunCalls: %v", err)
	}
	if ok, failures := checkCaps(Caps{MaxTokens: 50}, pooled); ok || len(failures) != 1 {
		t.Errorf("80 answer tokens under a 50-token cap: ok=%v failures=%v", ok, failures)
	}
	if ok, failures := checkCaps(Caps{P95LatencyMS: 500}, pooled); ok || len(failures) != 1 {
		t.Errorf("800ms under a 500ms cap: ok=%v failures=%v", ok, failures)
	}
}

// A run answered by two models has no single (provider, model) heading to
// certify under, exactly like a run SET that mixes them.
func TestServedUniformlyRefusesARunTwoModelsAnswered(t *testing.T) {
	pooled, err := poolRunCalls([]ai.Call{
		{Provider: "fake", ServedModel: "model-a"},
		{Provider: "fake", ServedModel: "model-b"},
	})
	if err != nil {
		t.Fatalf("poolRunCalls: %v", err)
	}
	err = pooled.servedUniformly()
	if err == nil {
		t.Fatal("want a refusal — one run cannot certify two served models")
	}
	if !strings.Contains(err.Error(), "model-a") || !strings.Contains(err.Error(), "model-b") {
		t.Fatalf("the refusal names neither identity to compare: %v", err)
	}
	if pooled.ServedModel != "model-a" {
		t.Fatalf("served model = %q, want the first call's", pooled.ServedModel)
	}
}

// A scored run that made no model call is a harness fault. Folded to zeroes it
// would enter the record as a free, instant, healthy run.
func TestPoolRunCallsRefusesARunWithNoCall(t *testing.T) {
	if _, err := poolRunCalls(nil); err == nil {
		t.Fatal("want a refusal — there is nothing to score")
	}
}

// The recorder's own half of the same claim: what a run recorded is every call
// it made, not the last batch to land.
func TestTerminalsSinceReturnsEveryCallARunMade(t *testing.T) {
	rec := newTraceRecorder()
	record := func(model string) {
		t.Helper()
		if err := rec.Record(wsContext(t), []ai.Call{
			{ServedModel: model + "-attempt"},
			{ServedModel: model, IsTerminal: true},
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	record("before-the-run")
	mark := rec.mark()
	record("call-one")
	record("call-two")

	calls, err := rec.terminalsSince(mark)
	if err != nil {
		t.Fatalf("terminalsSince: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want the 2 made since the mark", len(calls))
	}
	if calls[0].ServedModel != "call-one" || calls[1].ServedModel != "call-two" {
		t.Fatalf("calls are not the run's own, in order: %+v", calls)
	}
}

// A batch with no terminal attempt is a programmer bug in this package, and a
// dropped call is the accounting error terminalsSince exists to remove — so it
// is reported rather than skipped.
func TestTerminalsSinceRefusesACallItCannotAccount(t *testing.T) {
	rec := newTraceRecorder()
	if err := rec.Record(wsContext(t), []ai.Call{{ServedModel: "attempted, never served"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := rec.terminalsSince(0); err == nil {
		t.Fatal("want a refusal — the call carries no terminal attempt to account")
	}
	if _, err := rec.terminalsSince(99); err == nil {
		t.Fatal("want a refusal — a mark beyond what was recorded cannot name a run")
	}
}

// The integration half, through the real router pipeline: a site that answers
// in two calls, whose SECOND call falls back to another model. certifyTask must
// void the record — the answer this run produced came from two models, and the
// record would have named one.
func TestCertifyTaskVoidsARecordWhenOneRunWasAnsweredByTwoModels(t *testing.T) {
	candidateFake := ai.NewFakeClient().ScriptSteps(
		ai.FakeStep{Text: "the widget is blue and durable", ServedModel: "model-a"}, // call 1 of the run
		ai.FakeStep{Err: errors.New("cheap_cloud: transient provider error")},       // call 2 fails on the first rung
		ai.FakeStep{Text: "the widget is blue and durable", ServedModel: "model-b"}, // call 2 falls back and serves
	)
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90))

	sc := testScenarioOnSite("basic", retryVariant, wideBands)
	_, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, retryCensus(t),
		ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
		})
	if err == nil {
		t.Fatal("want an error — no record for a run half-answered by another model")
	}
	if !strings.Contains(err.Error(), "model-a") || !strings.Contains(err.Error(), "model-b") {
		t.Fatalf("error should name both identities, got %v", err)
	}
}

// The record's token numbers, end to end: the same request sent twice inside
// one run costs twice one request. A record that counted the last call would
// report half of what the run spent, and EstCostMicroUSD prices these means.
func TestCertifyTaskCountsEveryCallARunMade(t *testing.T) {
	oneCall := certifiedTokens(t, testCensus(t), testScenario("basic", wideBands), 1)
	twoCalls := certifiedTokens(t, retryCensus(t), testScenarioOnSite("basic", retryVariant, wideBands), 2)

	if oneCall == 0 {
		t.Fatal("the single-call run recorded no tokens, so the comparison below proves nothing")
	}
	if twoCalls != 2*oneCall {
		t.Fatalf("a two-call run recorded %d mean tokens where one call recorded %d — want exactly twice, since both calls send the same request",
			twoCalls, oneCall)
	}
}

// certifiedTokens certifies one scenario over `calls` scripted replies (one per
// call the site makes) and returns the record's mean token count.
func certifiedTokens(t *testing.T, census *aitasks.Registry, sc Scenario, calls int) int {
	t.Helper()
	replies := make([]string, calls)
	for i := range replies {
		replies[i] = "the widget is blue and durable"
	}
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, census,
		ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script(replies...))},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script(scoreJSON(90)))},
		})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.Runs != 1 {
		t.Fatalf("runs = %d, want 1 — the mean below is then the run's own total", rec.Runs)
	}
	return rec.MeanTokens
}
