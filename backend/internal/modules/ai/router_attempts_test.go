// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The grain-change contract (spec §4): a logical call's retries,
// degradations, and escalations land as one flush of several ai_call rows
// sharing a LogicalCallID, with exactly one IsTerminal — split out of
// router_tracing_test.go, which covers the pre-grain-change one-row-per-
// terminal tracing this behavior builds on.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// TestLadderFallbackBuffersOneLogicalCallWithTwoAttempts is the grain-change
// contract (spec §4): premium fails, cheap serves — that is ONE logical
// call spanning two attempt rows, not two independent ai_call rows. The
// failed rung is non-terminal with attempt_reason=provider_error; the rung
// that served is terminal; both share LogicalCallID and Task/CorrelationID.
func TestLadderFallbackBuffersOneLogicalCallWithTwoAttempts(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{
			TierPremium:    stubClient{err: errors.New("premium down")},
			TierCheapCloud: stubClient{resp: model.Response{Text: "cheap answer", OutputTokens: 2}},
		},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{
			TierPremium:    {provider: "anthropic", model: "claude-premium"},
			TierCheapCloud: {provider: "openai", model: "gpt-cheap"},
		},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	corr := ids.NewV7()
	ctx := principal.WithCorrelationID(wsCtx(), corr)
	if _, info, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierPremium, TierCheapCloud}, model.Request{}); err != nil || info.Tier != TierCheapCloud {
		t.Fatalf("cheap fallback: %v %+v", err, info)
	}
	if len(fcs.recorded) != 2 {
		t.Fatalf("want 2 attempt rows for one logical call, got %d: %+v", len(fcs.recorded), fcs.recorded)
	}
	first, second := fcs.recorded[0], fcs.recorded[1]
	if first.IsTerminal || first.Tier != TierPremium || first.AttemptReason != attemptReasonProviderError || first.Attempt != 1 {
		t.Fatalf("first attempt (the failed premium rung) wrong: %+v", first)
	}
	if !second.IsTerminal || second.Tier != TierCheapCloud || second.Attempt != 2 {
		t.Fatalf("second attempt (the served cheap rung) wrong: %+v", second)
	}
	if first.LogicalCallID != second.LogicalCallID {
		t.Fatalf("attempts of one logical call disagree on LogicalCallID: %+v vs %+v", first, second)
	}
	if first.CorrelationID == nil || *first.CorrelationID != corr || second.CorrelationID == nil || *second.CorrelationID != corr {
		t.Fatalf("both attempts must carry the request's correlation id: %+v %+v", first, second)
	}
}

// TestCacheHitStaysOneTerminalRow: a cache hit never walks the ladder, so
// it must produce exactly one terminal row — the grain change must not
// turn a single cache-served answer into a multi-attempt trace.
func TestCacheHitStaysOneTerminalRow(t *testing.T) {
	fcs := &fakeCallStore{}
	r := newTracingRouter(t, stubClient{resp: model.Response{Text: "first", OutputTokens: 1}}, fcs)
	ctx := wsCtx()
	req := model.Request{System: "same request"}
	if _, _, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierCheapCloud}, req); err != nil {
		t.Fatalf("first serve: %v", err)
	}
	fcs.recorded = nil // only the second (cached) call matters here
	_, info, err := r.serveCompletion(ctx, TaskColdStart, []Tier{TierCheapCloud}, req)
	if err != nil {
		t.Fatalf("cached serve: %v", err)
	}
	if !info.Cached {
		t.Fatal("expected the second identical call to be served from cache")
	}
	if len(fcs.recorded) != 1 || !fcs.recorded[0].IsTerminal || !fcs.recorded[0].CacheHit {
		t.Fatalf("cache hit must record exactly one terminal row, got %+v", fcs.recorded)
	}
}

// TestStructuredRetryChainSharesOneLogicalCall proves CompleteStructured's
// three possible attempts (default route, same-route retry with validator
// feedback, escalated-tier retry) are buffered as ONE logical call: the
// grain change must not fragment a schema-retry chain across unrelated
// ai_call rows. The middle (same-route) retry is non-terminal with
// attempt_reason=schema_invalid; the escalated rung that finally validates
// is terminal.
func TestStructuredRetryChainSharesOneLogicalCall(t *testing.T) {
	cheap := NewFakeClient().Script("bad one", "bad two")
	premium := NewFakeClient().Script(`{"rescued":true}`)
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: cheap, TierPremium: premium},
		nil, ProfileEUHosted, &memMeter{}, DefaultMonthlyTokens, fcs,
		map[Tier]routeMeta{
			TierCheapCloud: {provider: "fake", model: "fake-cheap"},
			TierPremium:    {provider: "fake", model: "fake-premium"},
		},
		false, nil,
	)
	resp, info, err := r.CompleteStructured(wsCtx(), TaskColdStart, structuredReq(), jsonObjectValidator)
	if err != nil || resp.Text != `{"rescued":true}` {
		t.Fatalf("escalation: %v %q", err, resp.Text)
	}
	if info.Tier != TierPremium {
		t.Fatalf("rescued attempt served from %s, want premium", info.Tier)
	}
	if len(fcs.recorded) != 3 {
		t.Fatalf("want 3 attempt rows sharing one logical call, got %d: %+v", len(fcs.recorded), fcs.recorded)
	}
	for i, c := range fcs.recorded {
		if c.LogicalCallID != fcs.recorded[0].LogicalCallID {
			t.Fatalf("attempt %d does not share the chain's LogicalCallID: %+v", i, c)
		}
		if c.Attempt != i+1 {
			t.Fatalf("attempt %d has Attempt=%d, want %d", i, c.Attempt, i+1)
		}
	}
	if fcs.recorded[0].IsTerminal || fcs.recorded[1].IsTerminal {
		t.Fatalf("only the last attempt may be terminal: %+v", fcs.recorded)
	}
	if !fcs.recorded[2].IsTerminal {
		t.Fatalf("the escalated, validated attempt must be terminal: %+v", fcs.recorded[2])
	}
	if fcs.recorded[1].AttemptReason != attemptReasonSchemaInvalid {
		t.Fatalf("the same-route retry must carry attempt_reason=schema_invalid: %+v", fcs.recorded[1])
	}
	if fcs.recorded[2].AttemptReason != attemptReasonSchemaInvalid {
		t.Fatalf("the escalated retry must carry attempt_reason=schema_invalid: %+v", fcs.recorded[2])
	}
}

// TestEmbedRecordsOneTerminalEmbeddingCall proves the embed lane traces
// exactly like a completion terminal — one row, Kind=embedding, IsTerminal,
// its own LogicalCallID — so the retention rule that ages embedding-kind
// rows out (privacy/retention.go) has something to select against.
func TestEmbedRecordsOneTerminalEmbeddingCall(t *testing.T) {
	fcs := &fakeCallStore{}
	embedder := NewFakeClient()
	r := assembleRouter(
		map[Tier]model.Client{}, embedder, ProfileEUHosted, &memMeter{}, DefaultMonthlyTokens, fcs,
		map[Tier]routeMeta{TierEmbedLane: {provider: "fake", model: "fake-embed"}},
		false, nil,
	)
	if _, err := r.Embed(wsCtx(), model.EmbedRequest{Inputs: []string{"embed me"}}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(fcs.recorded) != 1 {
		t.Fatalf("want exactly 1 recorded call, got %d: %+v", len(fcs.recorded), fcs.recorded)
	}
	got := fcs.recorded[0]
	if got.Kind != callKindEmbedding || !got.IsTerminal || got.Tier != TierEmbedLane || got.Attempt != 1 {
		t.Fatalf("embed call traced wrong: %+v", got)
	}
	if got.Provider != "fake" || got.ModelID != "fake-embed" {
		t.Fatalf("embed call missing its route metadata: %+v", got)
	}
	if (got.LogicalCallID == ids.UUID{}) {
		t.Fatal("embed call must mint its own LogicalCallID")
	}
}

// TestEmbedTraceTokensMatchMeteredUsage proves the I1 fix: the embed
// lane's ai_call trace row carries the SAME token estimate the meter
// records for the identical call, instead of a hardcoded 0. Before the
// fix, CostReport treated a zero-usage trace as free-by-construction (the
// "call failed before reaching the provider" case), so a paid embedding
// model priced to a silent $0 even though ai_usage showed real tokens —
// this test pins the trace and the meter to the same nonzero number so
// that misreading can't recur.
func TestEmbedTraceTokensMatchMeteredUsage(t *testing.T) {
	fcs := &fakeCallStore{}
	meter := &memMeter{}
	embedder := NewFakeClient()
	r := assembleRouter(
		map[Tier]model.Client{}, embedder, ProfileEUHosted, meter, DefaultMonthlyTokens, fcs,
		map[Tier]routeMeta{TierEmbedLane: {provider: "gemini", model: "gemini-embedding-001"}},
		false, nil,
	)
	inputs := []string{"a fairly long note to embed, long enough to estimate a nonzero token count"}
	if _, err := r.Embed(wsCtx(), model.EmbedRequest{Inputs: inputs}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	wantTokens := embedTokenEstimate(inputs)
	if wantTokens == 0 {
		t.Fatal("test fixture input estimates to 0 tokens — fixture bug, strengthen the input")
	}

	if len(fcs.recorded) != 1 {
		t.Fatalf("want exactly 1 recorded call, got %d: %+v", len(fcs.recorded), fcs.recorded)
	}
	trace := fcs.recorded[0]
	if trace.TokensIn != wantTokens {
		t.Fatalf("trace.TokensIn = %d, want %d (embedTokenEstimate) — the trace row must not read zero-usage while the meter charges real tokens", trace.TokensIn, wantTokens)
	}
	if trace.TokensOut != 0 || trace.CachedTokens != 0 || trace.CacheWriteTokens != 0 {
		t.Fatalf("embed trace must stay input-only, got %+v", trace)
	}

	if len(meter.records) != 1 {
		t.Fatalf("want exactly 1 metered usage record, got %d: %+v", len(meter.records), meter.records)
	}
	if meter.records[0].TokensIn != trace.TokensIn {
		t.Fatalf("meter TokensIn = %d, trace TokensIn = %d — the two must agree on what the call cost", meter.records[0].TokensIn, trace.TokensIn)
	}
}

// TestEmbedTraceStaysZeroUsageOnFailure proves the failure path is
// untouched by the fix: a call that never reached the provider still
// traces as free-by-construction (tokens_in=0), the same as before —
// only a SUCCESSFUL embed call gets the token estimate stamped onto its
// trace.
func TestEmbedTraceStaysZeroUsageOnFailure(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		// stubClient.Embed always errors regardless of its resp/err fields
		// (router_tracing_test.go) — exactly the "never reaches the
		// provider" case this test needs.
		map[Tier]model.Client{}, stubClient{}, ProfileEUHosted, &memMeter{}, DefaultMonthlyTokens, fcs,
		map[Tier]routeMeta{TierEmbedLane: {provider: "gemini", model: "gemini-embedding-001"}},
		false, nil,
	)
	if _, err := r.Embed(wsCtx(), model.EmbedRequest{Inputs: []string{"note that never reaches the provider"}}); err == nil {
		t.Fatal("expected the embed call to fail")
	}
	if len(fcs.recorded) != 1 {
		t.Fatalf("want exactly 1 recorded call, got %d: %+v", len(fcs.recorded), fcs.recorded)
	}
	if got := fcs.recorded[0].TokensIn; got != 0 {
		t.Fatalf("a failed embed call must trace as zero-usage (free by construction), got TokensIn=%d", got)
	}
}

// TestMetricsCountOneCallPerLogicalCallNotPerAttempt is the Prometheus half
// of the grain change: margince_ai_calls_total must bump once per served-or-
// failed decision, not once per ladder rung it took to get there — a
// retried/escalated call must not inflate the counter or the error rate.
func TestMetricsCountOneCallPerLogicalCallNotPerAttempt(t *testing.T) {
	fcs := &fakeCallStore{}
	r := assembleRouter(
		map[Tier]model.Client{
			TierPremium:    stubClient{err: errors.New("premium down")},
			TierCheapCloud: stubClient{resp: model.Response{Text: "cheap answer", OutputTokens: 2}},
		},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, fcs,
		map[Tier]routeMeta{
			TierPremium:    {provider: "anthropic", model: "claude-premium"},
			TierCheapCloud: {provider: "openai", model: "gpt-cheap"},
		},
		false, nil,
	)
	r.now = func() time.Time { return time.Unix(0, 0) }
	// A private collector, not the process-wide sharedCallMetrics, so this
	// assertion never races other tests' observations.
	r.metrics = newCallMetrics()
	if _, info, err := r.serveCompletion(wsCtx(), TaskColdStart, []Tier{TierPremium, TierCheapCloud}, model.Request{}); err != nil || info.Tier != TierCheapCloud {
		t.Fatalf("cheap fallback: %v %+v", err, info)
	}
	if len(fcs.recorded) != 2 {
		t.Fatalf("expected 2 buffered attempt rows, got %d", len(fcs.recorded))
	}
	var b strings.Builder
	r.metrics.WritePrometheus(&b)
	out := b.String()
	if !strings.Contains(out, `margince_ai_calls_total{provider="openai",task="cold_start",tier="cheap_cloud"} 1`) {
		t.Fatalf("want exactly one counted call on the served tier, got:\n%s", out)
	}
	if strings.Contains(out, `provider="anthropic"`) {
		t.Fatalf("the non-terminal failed rung must not surface in /metrics at all:\n%s", out)
	}
}
