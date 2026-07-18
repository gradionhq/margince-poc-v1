// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Embedding lane labels for metering: not a chat tier (§1.1).
const (
	TaskEmbeddings Task = "embeddings"
	TierEmbedLane  Tier = "embed"
)

// resultCacheTTL bounds staleness for cached completions (§6: TTL and
// record-change invalidation; the workspace-scoped Invalidate hook is
// the latter's entry point).
const resultCacheTTL = 15 * time.Minute

// traceWriteTimeout bounds the deferred ai_call write that runs on a
// context detached from the request's cancellation.
const traceWriteTimeout = 5 * time.Second

// routeMeta is the provider/model identity per tier, retained from the
// routing config so every ai_call row and RouteInfo can name what served
// the call without reaching into the opaque Client.
type routeMeta struct {
	provider string
	model    string
}

// Router is the tiered routing engine (B-EP06.4): tasks name tiers,
// tiers resolve to bound Clients, the budget guardrail bends the route
// before the call, and every call lands in the meter. This is the one
// place routing policy lives — callers never pick a model.
type Router struct {
	clients         map[Tier]model.Client
	embedder        model.Client
	profile         Profile
	meter           usageStore
	budget          BudgetPolicy
	stripper        model.SecretStripper
	cache           *resultCache
	calls           callStore
	routeMeta       map[Tier]routeMeta
	capturePayloads bool
	log             *slog.Logger
	metrics         *callMetrics
	now             func() time.Time
	// cacheOff disables the §6 result cache entirely (ai.WithoutResultCache):
	// the cert lane and scripted repeat-call tests need every call to reach
	// the model, not collapse onto a cached answer.
	cacheOff bool
}

// RouteInfo tells the caller how its request was actually served — the
// honest "reduced quality" signal the UI surfaces in economy mode, plus
// the provider/model identity the agent-run trace records (RUNNER-AC-4).
type RouteInfo struct {
	Tier     Tier
	Provider string
	ModelID  string
	Degraded bool
	Cached   bool
}

// NewRouter builds the production router from a validated routing config.
// calls traces every completion terminal (ai_call); capturePayloads gates
// the Layer-3 content capture; log carries router observability.
func NewRouter(cfg RoutingConfig, meter *Meter, budget BudgetPolicy, calls callStore, capturePayloads bool, log *slog.Logger) (*Router, error) {
	clients, embedder, err := cfg.buildClients()
	if err != nil {
		return nil, err
	}
	meta := make(map[Tier]routeMeta, len(cfg.Tiers))
	for tier, binding := range cfg.Tiers {
		meta[tier] = routeMeta{provider: binding.Provider, model: binding.Model}
	}
	return assembleRouter(clients, embedder, cfg.Profile, meter, budget, calls, meta, capturePayloads, log), nil
}

// assembleRouter is the seam unit tests inject fakes through.
func assembleRouter(clients map[Tier]model.Client, embedder model.Client, profile Profile, meter usageStore, budget BudgetPolicy, calls callStore, meta map[Tier]routeMeta, capturePayloads bool, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{
		clients:         clients,
		embedder:        embedder,
		profile:         profile,
		meter:           meter,
		budget:          budget,
		stripper:        NewSecretStripper(),
		cache:           newResultCache(resultCacheTTL),
		calls:           calls,
		routeMeta:       meta,
		capturePayloads: capturePayloads,
		log:             log,
		// Every Router shares the one process-wide collector (metrics.go):
		// coldStartOptions and offerDraftOptions each mint their own Router
		// over the same routing config, and /metrics must report one honest
		// total across both, rendered exactly once.
		metrics: sharedCallMetrics,
		now:     time.Now,
	}
}

// Complete routes one task to a completion. The request names no model:
// the resolved tier's binding supplies it.
func (r *Router) Complete(ctx context.Context, task Task, req model.Request) (model.Response, RouteInfo, error) {
	ladder, ok := taskLadders[task]
	if !ok {
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: unknown task %q", task)
	}
	return r.serveCompletion(ctx, task, ladder, req)
}

// serveCompletion serves one call over an explicit ladder — Complete
// passes the task default, the structured-output pipeline passes an
// escalated suffix.
func (r *Router) serveCompletion(ctx context.Context, task Task, ladder []Tier, req model.Request) (resp model.Response, info RouteInfo, err error) {
	rawWS, ok := principal.WorkspaceID(ctx)
	if !ok {
		// No workspace ⇒ no RLS-writable trace row; fail before installing
		// the recorder so we never attempt a tenant write outside a tenant.
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: task %s outside workspace context", task)
	}
	wsID := ids.From[ids.WorkspaceKind](rawWS)
	if req.SecretStripper == nil {
		req.SecretStripper = r.stripper
	}

	key, keyErr := cacheKey(wsID, task, req)
	if keyErr != nil {
		return model.Response{}, RouteInfo{}, keyErr
	}

	// Every ROUTING terminal below is traced: one ai_call row for the served
	// call, the cache hit, or the failure. The earlier workspace-context and
	// cache-key failures return before this recorder is installed and are not
	// traced (no RLS-writable row exists yet, and no route was attempted). The
	// recorder is best-effort — a trace-write failure is logged, never
	// returned, so observability can't become a new way for a working model
	// call to fail (contrast the meter, which fails loudly to protect the
	// budget guardrail).
	start := r.now()
	trace := Call{Task: task, RequestFingerprint: key}
	if cid, ok := principal.CorrelationID(ctx); ok {
		trace.CorrelationID = &cid
	}
	if rid, ok := principal.AgentRunID(ctx); ok {
		trace.AgentRunID = &rid
	}
	defer func() {
		trace.LatencyMS = r.now().Sub(start).Milliseconds()
		trace.ErrorSentinel = classifyError(err)
		// Stamped from the Router's own posture, not per-request: a
		// cache-off Router never consulted the cache for this call, so
		// every one of its traces says so, hit-or-miss.
		trace.CacheOff = r.cacheOff
		if m := r.routeMeta[trace.Tier]; trace.Tier != "" {
			trace.Provider, trace.ModelID = m.provider, m.model
		}
		// The trace write must outlive request cancellation: a timed-out or
		// canceled call is exactly the terminal worth recording, and the
		// workspace GUC values ride context values, which WithoutCancel
		// preserves. The bound keeps a dead trace store from pinning the
		// request goroutine.
		traceCtx, cancelTrace := context.WithTimeout(context.WithoutCancel(ctx), traceWriteTimeout)
		defer cancelTrace()
		r.observeCall(traceCtx, trace, req, resp)
	}()

	ladder, degraded, budgetErr := r.applyBudget(ctx, task, wsID, ladder)
	if budgetErr != nil {
		return model.Response{}, RouteInfo{}, budgetErr
	}
	trace.Degraded = degraded
	ladder = r.applyProfile(ladder)

	// A cached answer only serves when its tier is still on the adjusted
	// ladder: after a budget band tightened or the profile remapped the
	// route, a premium-tier entry must not smuggle premium output into an
	// economy route. The stale entry stays put — TTL ages it out, and the
	// band may relax within its lifetime. A cache-off Router (§ cert lane,
	// scripted repeat-call tests) never consults it: every call must reach
	// the model.
	if cached, tier, hit := r.cache.get(key, wsID); !r.cacheOff && hit && tierOnLadder(ladder, tier) {
		trace.Tier, trace.CacheHit = tier, true
		if meterErr := r.meter.Record(ctx, Usage{Task: task, Tier: tier, Cached: true}); meterErr != nil {
			// A served (cache-hit) call whose metering failed: label it as a
			// metering failure, not a provider error — the tier is already set.
			return model.Response{}, RouteInfo{}, fmt.Errorf("ai: metering cache hit: %w", errors.Join(errMeteringFailed, meterErr))
		}
		m := r.routeMeta[tier]
		return cached, RouteInfo{Tier: tier, Provider: m.provider, ModelID: m.model, Degraded: degraded, Cached: true}, nil
	}

	out, tier, served, ladderErr := r.attemptLadder(ctx, task, ladder, req, key, wsID)
	// Stamp tier and usage even when the ladder returns an error: a
	// metering failure of a successfully-served call still spent provider
	// tokens on a real tier, and an all-rungs-failed walk names the last
	// tier attempted — the trace records what actually happened, not an
	// empty terminal.
	trace.Tier = tier
	trace.TokensIn, trace.TokensOut = out.InputTokens, out.OutputTokens
	trace.ReasoningTokens, trace.CachedTokens = out.ReasoningTokens, out.CachedTokens
	if ladderErr != nil {
		return model.Response{}, RouteInfo{}, ladderErr
	}
	if served {
		m := r.routeMeta[tier]
		return out, RouteInfo{Tier: tier, Provider: m.provider, ModelID: m.model, Degraded: degraded}, nil
	}
	// The honest degraded state (§4.3): no bound model can serve this.
	return model.Response{}, RouteInfo{}, fmt.Errorf("ai: no bound tier can serve %s in profile %s", task, r.profile)
}

// attemptLadder walks the (already budget- and profile-adjusted) tier
// ladder, calling the first bound client that succeeds. A provider error
// falls through to the next rung (§1.2); the last rung's failure is what
// the caller sees. On success the served response is metered (failing
// loudly — unmetered spend would quietly hollow out the budget guardrail)
// and cached before it is returned to serveCompletion for tracing.
func (r *Router) attemptLadder(ctx context.Context, task Task, ladder []Tier, req model.Request, key string, wsID ids.WorkspaceID) (resp model.Response, tier Tier, served bool, err error) {
	var lastErr error
	var lastTier Tier
	for _, t := range ladder {
		client, bound := r.clients[t]
		if !bound {
			continue
		}
		lastTier = t
		out, callErr := client.Complete(ctx, req)
		if callErr != nil {
			lastErr = callErr
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

// observeCall writes the ai_call trace row, emits router slog, and bumps
// the /metrics counters. Trace-write failure is logged, not returned.
func (r *Router) observeCall(ctx context.Context, c Call, req model.Request, resp model.Response) {
	if r.capturePayloads && c.ErrorSentinel == "" && !c.CacheHit {
		if p, perr := r.buildPayload(ctx, req, resp); perr != nil {
			r.log.WarnContext(ctx, "ai: payload capture failed", "err", perr)
		} else {
			c.Payload = p
		}
	}
	if r.metrics != nil {
		r.metrics.observe(c)
	}
	r.log.InfoContext(ctx, "ai.call",
		"task", string(c.Task), "tier", string(c.Tier), "provider", c.Provider,
		"tokens_in", c.TokensIn, "tokens_out", c.TokensOut, "latency_ms", c.LatencyMS,
		"cache_hit", c.CacheHit, "degraded", c.Degraded, "error", c.ErrorSentinel)
	if r.calls == nil {
		return
	}
	if err := r.calls.Record(ctx, c); err != nil {
		r.log.ErrorContext(ctx, "ai: recording call trace failed", "task", string(c.Task), "err", err)
	}
}

// WriteMetrics renders the router's AI counters in Prometheus text form —
// the composition layer wires it into the /metrics handler.
func (r *Router) WriteMetrics(w io.Writer) { r.metrics.WritePrometheus(w) }

// Embed routes the embedding lane. Inputs are stripped before egress —
// the EmbedRequest carries no per-request hook, so the router is the
// enforcement point here.
func (r *Router) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	if _, ok := principal.WorkspaceID(ctx); !ok {
		return model.Embeddings{}, fmt.Errorf("ai: embeddings outside workspace context")
	}
	stripped := make([]string, len(req.Inputs))
	for i, input := range req.Inputs {
		clean, _, err := r.stripper.Strip(ctx, []byte(input))
		if err != nil {
			return model.Embeddings{}, fmt.Errorf("ai: stripping embed input: %w", err)
		}
		stripped[i] = string(clean)
	}
	req.Inputs = stripped
	res, err := r.embedder.Embed(ctx, req)
	if err != nil {
		return model.Embeddings{}, err
	}
	if err := r.meter.Record(ctx, Usage{Task: TaskEmbeddings, Tier: TierEmbedLane, TokensIn: embedTokenEstimate(req.Inputs)}); err != nil {
		return model.Embeddings{}, fmt.Errorf("ai: call served but metering failed: %w", err)
	}
	return res, nil
}

// EmbedDims reports the embedding lane's vector width.
func (r *Router) EmbedDims() int { return r.embedder.Caps().EmbedDims }

// Invalidate drops a workspace's cached results — the hook the §6
// record-change invalidation rides (wired from event consumers).
func (r *Router) Invalidate(workspaceID ids.WorkspaceID) { r.cache.invalidate(workspaceID) }

// applyBudget bends the ladder per §1.3: soft-degrade one tier at 80%,
// queue non-interactive / pin interactive to local-small at 100%.
func (r *Router) applyBudget(ctx context.Context, task Task, wsID ids.WorkspaceID, ladder []Tier) ([]Tier, bool, error) {
	budgetTokens, err := r.budget.MonthlyTokenBudget(ctx, wsID)
	if err != nil {
		return nil, false, fmt.Errorf("ai: budget policy: %w", err)
	}
	if budgetTokens <= 0 {
		// Fail closed on misconfiguration — an accidental zero budget must
		// not read as "unlimited".
		return nil, false, fmt.Errorf("ai: workspace has a non-positive token budget (%d)", budgetTokens)
	}
	spent, err := r.meter.MonthTokens(ctx)
	if err != nil {
		return nil, false, err
	}
	utilization := float64(spent) / float64(budgetTokens)
	switch {
	case utilization >= queueUtilization:
		if nonInteractive[task] {
			return nil, false, fmt.Errorf("%w: task %s", ErrBudgetExhausted, task)
		}
		return []Tier{TierLocalSmall}, true, nil
	case utilization >= degradeUtilization:
		degradedLadder := make([]Tier, 0, len(ladder))
		for _, tier := range ladder {
			demoted := degradeTo[tier]
			if len(degradedLadder) == 0 || degradedLadder[len(degradedLadder)-1] != demoted {
				degradedLadder = append(degradedLadder, demoted)
			}
		}
		return degradedLadder, true, nil
	default:
		return ladder, false, nil
	}
}

// applyProfile remaps cloud rungs to local ones under sovereign: P7
// zero-egress holds by construction because a cloud tier is simply
// never selected (validation already refused cloud bindings).
func (r *Router) applyProfile(ladder []Tier) []Tier {
	if r.profile != ProfileSovereign {
		return ladder
	}
	remapped := make([]Tier, 0, len(ladder))
	for _, tier := range ladder {
		if tier == TierCheapCloud || tier == TierPremium {
			if _, ok := r.clients[TierLocalLarge]; ok {
				tier = TierLocalLarge
			} else {
				tier = TierLocalSmall
			}
		}
		if len(remapped) == 0 || remapped[len(remapped)-1] != tier {
			remapped = append(remapped, tier)
		}
	}
	return remapped
}

// embedTokenEstimate meters the embed lane by the ~4-bytes-per-token
// heuristic; local embedders report no usage counts.
func embedTokenEstimate(inputs []string) int {
	total := 0
	for _, s := range inputs {
		total += len(s) / 4
	}
	return total
}

// cacheKey covers EVERY completion-shaping input (model override, system,
// messages, tools, max tokens, response schema, attachments, and provider
// options) via a collision-resistant digest,
// prefixed with the plaintext workspace id: a hash collision may spoil a cache
// hit but can never cross a tenant boundary, because the workspace segment is
// compared literally (and re-checked against the stored entry on read).
// Attachments and provider options MUST be in the digest — otherwise two calls
// with identical prompt text but a different attached document (or a different
// reasoning/thinking knob) collide, and the second is served the first's answer.
func cacheKey(wsID ids.WorkspaceID, task Task, req model.Request) (string, error) {
	material, err := json.Marshal(struct {
		Model           string                     `json:"model"`
		System          string                     `json:"system"`
		Messages        []model.Message            `json:"messages"`
		Tools           []model.ToolDef            `json:"tools"`
		MaxTokens       int                        `json:"max_tokens"`
		ResponseSchema  json.RawMessage            `json:"response_schema"`
		Attachments     []model.Attachment         `json:"attachments"`
		ProviderOptions map[string]json.RawMessage `json:"provider_options"`
	}{req.Model, req.System, req.Messages, req.Tools, req.MaxTokens, req.ResponseSchema, req.Attachments, req.ProviderOptions})
	if err != nil {
		// A ProviderOptions namespace carrying invalid JSON would otherwise
		// marshal to nil and collapse every such request onto one cache key —
		// fail loudly instead of serving a collided answer.
		return "", fmt.Errorf("ai: cache key: %w", err)
	}
	sum := sha256.Sum256(material)
	return wsID.String() + "|" + string(task) + "|" + hex.EncodeToString(sum[:]), nil
}
