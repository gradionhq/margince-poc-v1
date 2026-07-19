// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The ai_call trace emission path: served-identity resolution, the
// per-rung buffering attemptLadder does as it walks a tier ladder, and the
// flush that turns one logicalCall's buffered attempts into a single
// Record call plus the router's slog/metrics observation (spec §4). Router
// itself (router.go) owns the routing DECISION — which tier, whether
// cached, whether degraded; this file owns recording what actually
// happened.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Served-identity sources for Call.ServedIdentitySource: how trustworthy a
// provider's model.Response.ServedModel is. servedIdentitySourceResponse is a
// dedicated wire field the vendor sets independently ("model" on Anthropic/
// Ollama/OpenAI, "modelVersion" on Gemini, the fake's own literal).
// servedIdentitySourceEcho is the generic OpenAI-compatible wire
// (openai_compatible, vllm) whose "model" field is only ever the request's
// own model back-reflected — never confirmed against what actually generated
// the completion. servedIdentitySourceConfigured is the fallback when the
// provider reported no identity at all.
const (
	servedIdentitySourceResponse   = "response"
	servedIdentitySourceEcho       = "echo"
	servedIdentitySourceConfigured = "configured"
)

// servedSource maps a provider to its served-identity source.
var servedSource = map[string]string{
	providerAnthropic:        servedIdentitySourceResponse,
	providerOllama:           servedIdentitySourceResponse,
	providerGemini:           servedIdentitySourceResponse,
	providerOpenAI:           servedIdentitySourceResponse,
	providerOpenAICompatible: servedIdentitySourceEcho,
	providerVLLM:             servedIdentitySourceEcho,
	ProviderFake:             servedIdentitySourceResponse,
}

// servedIdentity resolves a trace's served-model fields: the response's own
// reported identity wins, tagged with how trustworthy that report is; an empty
// report (the provider named none, or the call never reached a provider at
// all — a total ladder failure) falls back to the tier's configured binding,
// honestly labeled servedIdentitySourceConfigured rather than passed off as
// confirmed.
func servedIdentity(provider, configuredModel, respServedModel string) (servedModel, source string) {
	if respServedModel == "" {
		return configuredModel, servedIdentitySourceConfigured
	}
	return respServedModel, servedSource[provider]
}

// finalizeAttempt completes trace from this attempt's outcome — latency,
// error sentinel, resolved route identity, best-effort payload capture —
// and appends it to lc. Split out of serveAttempt's deferred closure so
// this branching lands on its own function, not serveAttempt's.
func (r *Router) finalizeAttempt(ctx context.Context, lc *logicalCall, trace *Call, req model.Request, resp model.Response, callErr error, start time.Time) {
	trace.LatencyMS = r.now().Sub(start).Milliseconds()
	trace.ErrorSentinel = classifyError(callErr)
	if m := r.routeMeta[trace.Tier]; trace.Tier != "" {
		trace.Provider, trace.ModelID = m.provider, m.model
	}
	trace.ServedModel, trace.ServedIdentitySource = servedIdentity(trace.Provider, trace.ModelID, resp.ServedModel)
	// Payload capture is best-effort and, like the trace write itself, must
	// not become a new way for a working model call to fail (contrast the
	// meter, which fails loudly to protect the budget guardrail). flush()
	// strips it later if a further attempt supersedes this one — only the
	// terminal row keeps it.
	if r.capturePayloads && trace.ErrorSentinel == "" && !trace.CacheHit {
		if p, perr := r.buildPayload(ctx, req, resp); perr != nil {
			r.log.WarnContext(ctx, "ai: payload capture failed", "err", perr)
		} else {
			trace.Payload = p
		}
	}
	lc.append(*trace)
}

// serveCacheHit completes a served-from-cache attempt: meter it as a cache
// hit and return the cached response. Split out of serveAttempt purely to
// keep serveAttempt's own branching count down.
func (r *Router) serveCacheHit(ctx context.Context, trace *Call, task Task, tier Tier, cached model.Response, degraded bool) (model.Response, RouteInfo, error) {
	trace.Tier, trace.CacheHit = tier, true
	if meterErr := r.meter.Record(ctx, Usage{Task: task, Tier: tier, Cached: true}); meterErr != nil {
		// A served (cache-hit) call whose metering failed: label it as a
		// metering failure, not a provider error — the tier is already set.
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: metering cache hit: %w", errors.Join(errMeteringFailed, meterErr))
	}
	m := r.routeMeta[tier]
	return cached, RouteInfo{Tier: tier, Provider: m.provider, ModelID: m.model, Degraded: degraded, Cached: true}, nil
}

// attemptLadder walks the (already budget- and profile-adjusted) tier
// ladder, calling the first bound client that succeeds. A provider error
// falls through to the next rung (§1.2); the last rung's failure is what
// the caller sees. Every rung EXCEPT the last one tried appends its own
// non-terminal Call to lc as the walk moves past it — the last rung's own
// outcome (success or the aggregate "every bound tier failed" error) is
// what serveAttempt's own deferred trace records, so it is never
// double-counted here. On success the served response is metered (failing
// loudly — unmetered spend would quietly hollow out the budget guardrail)
// and cached before it is returned to serveAttempt for tracing.
func (r *Router) attemptLadder(ctx context.Context, lc *logicalCall, base Call, task Task, ladder []Tier, req model.Request, key string, wsID ids.WorkspaceID, start time.Time) (resp model.Response, tier Tier, served bool, err error) {
	var boundRungs []Tier
	for _, t := range ladder {
		if _, ok := r.clients[t]; ok {
			boundRungs = append(boundRungs, t)
		}
	}
	var lastErr error
	var lastTier Tier
	for i, t := range boundRungs {
		out, callErr := r.clients[t].Complete(ctx, req)
		if callErr != nil {
			lastErr, lastTier = callErr, t
			if i < len(boundRungs)-1 {
				lc.append(r.traceForFailedRung(base, t, callErr, start))
			}
			continue
		}
		if meterErr := r.meter.Record(ctx, Usage{Task: task, Tier: t, TokensIn: out.InputTokens, TokensOut: out.OutputTokens, CachedTokens: out.CachedTokens, ReasoningTokens: out.ReasoningTokens}); meterErr != nil {
			// Return the served response and tier even though the call fails:
			// provider tokens were spent, and the trace must bill them to the
			// tier that answered. errMeteringFailed keeps classifyError from
			// mislabeling this a provider error.
			return out, t, false, fmt.Errorf("ai: call served but metering failed: %w", errors.Join(errMeteringFailed, meterErr))
		}
		if !r.cacheOff {
			r.cache.put(key, wsID, out, t)
		}
		return out, t, true, nil
	}
	if lastErr != nil {
		// lastTier names the rung whose failure the caller sees, so the
		// trace records where the walk died instead of an empty tier.
		return model.Response{}, lastTier, false, fmt.Errorf("ai: every bound tier failed for %s: %w", task, lastErr)
	}
	return model.Response{}, "", false, nil
}

// traceForFailedRung builds the non-terminal Call for a ladder rung the
// walk moved past — cloning base's request-level fields (task,
// correlation, fingerprint, cache-off) and filling in this rung's own
// tier, provider/model, latency-so-far, and provider_error sentinel.
func (r *Router) traceForFailedRung(base Call, t Tier, callErr error, start time.Time) Call {
	c := base
	c.Tier = t
	if m, ok := r.routeMeta[t]; ok {
		c.Provider, c.ModelID = m.provider, m.model
	}
	c.ErrorSentinel = classifyError(callErr)
	c.ServedModel, c.ServedIdentitySource = servedIdentity(c.Provider, c.ModelID, "")
	c.LatencyMS = r.now().Sub(start).Milliseconds()
	c.AttemptReason = attemptReasonProviderError
	return c
}

// tierOnLadder reports whether t survives on the budget- and
// profile-adjusted ladder.
func tierOnLadder(ladder []Tier, t Tier) bool {
	for _, rung := range ladder {
		if rung == t {
			return true
		}
	}
	return false
}

// flushDetached wraps flush in a context that outlives the request: a
// timed-out or canceled call is exactly the terminal worth recording, and
// the workspace GUC values ride context values, which WithoutCancel
// preserves. The bound keeps a dead trace store from pinning the caller's
// goroutine.
func (r *Router) flushDetached(ctx context.Context, lc *logicalCall) {
	traceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), traceWriteTimeout)
	defer cancel()
	r.flush(traceCtx, lc)
}

// flush writes every buffered attempt of lc in one Record call, emits
// router slog and bumps the /metrics counters from the TERMINAL attempt
// only — a retried or escalated logical call must count once against
// margince_ai_calls_total, not once per rung it walked. Record failure is
// logged, never returned: observability can't become a new way for a
// working model call to fail.
func (r *Router) flush(ctx context.Context, lc *logicalCall) {
	if lc == nil || len(lc.attempts) == 0 {
		return
	}
	for i := range lc.attempts {
		if !lc.attempts[i].IsTerminal {
			// Payload FKs the terminal row only — strip it from any
			// attempt a later one superseded.
			lc.attempts[i].Payload = nil
		}
	}
	term := lc.terminal()
	if r.metrics != nil {
		r.metrics.observe(term)
	}
	r.log.InfoContext(ctx, "ai.call",
		"task", string(term.Task), "tier", string(term.Tier), "provider", term.Provider,
		"tokens_in", term.TokensIn, "tokens_out", term.TokensOut, "latency_ms", term.LatencyMS,
		"cache_hit", term.CacheHit, "degraded", term.Degraded, "error", term.ErrorSentinel,
		"attempts", len(lc.attempts))
	if r.calls == nil {
		return
	}
	if r.configHash != "" {
		if err := r.calls.EnsureConfig(ctx, r.configSnapshot); err != nil {
			// Best-effort enrichment: a working call must still be traced
			// even when the config-dimension write fails — just without a
			// config_hash this once.
			r.log.WarnContext(ctx, "ai: ensuring config snapshot failed — tracing without config_hash", "err", err)
		} else {
			hash := r.configHash
			for i := range lc.attempts {
				lc.attempts[i].ConfigHash = &hash
			}
		}
	}
	if err := r.calls.Record(ctx, lc.attempts); err != nil {
		r.log.ErrorContext(ctx, "ai: recording call trace failed", "task", string(term.Task), "err", err)
	}
}

// WriteMetrics renders the router's AI counters in Prometheus text form —
// the composition layer wires it into the /metrics handler.
func (r *Router) WriteMetrics(w io.Writer) { r.metrics.WritePrometheus(w) }
