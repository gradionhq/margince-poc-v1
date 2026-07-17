// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
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

// Router is the tiered routing engine (B-EP06.4): tasks name tiers,
// tiers resolve to bound Clients, the budget guardrail bends the route
// before the call, and every call lands in the meter. This is the one
// place routing policy lives — callers never pick a model.
type Router struct {
	clients  map[Tier]model.Client
	embedder model.Client
	profile  Profile
	meter    usageStore
	budget   BudgetPolicy
	stripper model.SecretStripper
	cache    *resultCache
}

// RouteInfo tells the caller how its request was actually served — the
// honest "reduced quality" signal the UI surfaces in economy mode.
type RouteInfo struct {
	Tier     Tier
	Degraded bool
	Cached   bool
}

func NewRouter(cfg RoutingConfig, meter *Meter, budget BudgetPolicy) (*Router, error) {
	clients, embedder, err := cfg.buildClients()
	if err != nil {
		return nil, err
	}
	return newRouter(clients, embedder, cfg.Profile, meter, budget), nil
}

// newRouter is the seam unit tests inject fakes through.
func newRouter(clients map[Tier]model.Client, embedder model.Client, profile Profile, meter usageStore, budget BudgetPolicy) *Router {
	return &Router{
		clients:  clients,
		embedder: embedder,
		profile:  profile,
		meter:    meter,
		budget:   budget,
		stripper: NewSecretStripper(),
		cache:    newResultCache(resultCacheTTL),
	}
}

// Complete routes one task to a completion. The request names no model:
// the resolved tier's binding supplies it.
func (r *Router) Complete(ctx context.Context, task Task, req model.Request) (model.Response, RouteInfo, error) {
	ladder, ok := taskLadders[task]
	if !ok {
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: unknown task %q", task)
	}
	return r.complete(ctx, task, ladder, req)
}

// complete serves one call over an explicit ladder — Complete passes
// the task default, the structured-output pipeline passes an escalated
// suffix.
func (r *Router) complete(ctx context.Context, task Task, ladder []Tier, req model.Request) (model.Response, RouteInfo, error) {
	rawWS, ok := principal.WorkspaceID(ctx)
	if !ok {
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: task %s outside workspace context", task)
	}
	wsID := ids.From[ids.WorkspaceKind](rawWS)
	if req.SecretStripper == nil {
		req.SecretStripper = r.stripper
	}

	ladder, degraded, err := r.applyBudget(ctx, task, wsID, ladder)
	if err != nil {
		return model.Response{}, RouteInfo{}, err
	}
	ladder = r.applyProfile(ladder)

	key := cacheKey(wsID, task, req)
	if resp, tier, hit := r.cache.get(key, wsID); hit {
		if err := r.meter.Record(ctx, Usage{Task: task, Tier: tier, Cached: true}); err != nil {
			return model.Response{}, RouteInfo{}, fmt.Errorf("ai: metering cache hit: %w", err)
		}
		return resp, RouteInfo{Tier: tier, Degraded: degraded, Cached: true}, nil
	}

	var lastErr error
	for _, tier := range ladder {
		client, bound := r.clients[tier]
		if !bound {
			continue
		}
		resp, err := client.Complete(ctx, req)
		if err != nil {
			// Fallback fires on provider error (§1.2); the last rung's
			// failure is what the caller sees.
			lastErr = err
			continue
		}
		if err := r.meter.Record(ctx, Usage{Task: task, Tier: tier, TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens, CachedTokens: resp.CachedTokens, ReasoningTokens: resp.ReasoningTokens}); err != nil {
			// The tokens are spent either way, but unmetered spend would
			// quietly hollow out the budget guardrail — fail loudly and
			// let the caller retry into a metered call.
			return model.Response{}, RouteInfo{}, fmt.Errorf("ai: call served but metering failed: %w", err)
		}
		r.cache.put(key, wsID, resp, tier)
		return resp, RouteInfo{Tier: tier, Degraded: degraded}, nil
	}
	if lastErr != nil {
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: every bound tier failed for %s: %w", task, lastErr)
	}
	// The honest degraded state (§4.3): no bound model can serve this —
	// tell the caller instead of silently egressing or fabricating.
	return model.Response{}, RouteInfo{}, fmt.Errorf("ai: no bound tier can serve %s in profile %s", task, r.profile)
}

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

// cacheKey covers EVERY completion-shaping input (system, messages, tools, max
// tokens, attachments, and provider options) via a collision-resistant digest,
// prefixed with the plaintext workspace id: a hash collision may spoil a cache
// hit but can never cross a tenant boundary, because the workspace segment is
// compared literally (and re-checked against the stored entry on read).
// Attachments and provider options MUST be in the digest — otherwise two calls
// with identical prompt text but a different attached document (or a different
// reasoning/thinking knob) collide, and the second is served the first's answer.
func cacheKey(wsID ids.WorkspaceID, task Task, req model.Request) string {
	material, _ := json.Marshal(struct {
		System          string                     `json:"system"`
		Messages        []model.Message            `json:"messages"`
		Tools           []model.ToolDef            `json:"tools"`
		MaxTokens       int                        `json:"max_tokens"`
		Attachments     []model.Attachment         `json:"attachments"`
		ProviderOptions map[string]json.RawMessage `json:"provider_options"`
	}{req.System, req.Messages, req.Tools, req.MaxTokens, req.Attachments, req.ProviderOptions})
	sum := sha256.Sum256(material)
	return wsID.String() + "|" + string(task) + "|" + hex.EncodeToString(sum[:])
}

// resultCache is the §6 result cache: workspace_id is part of the key
// (RT-AI-M7 — two tenants with identical inputs must never share an
// answer), TTL-bounded, with a per-workspace invalidation hook.
type resultCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cacheEntry
}

type cacheEntry struct {
	workspaceID ids.WorkspaceID
	resp        model.Response
	tier        Tier
	expires     time.Time
}

func newResultCache(ttl time.Duration) *resultCache {
	return &resultCache{ttl: ttl, now: time.Now, entries: map[string]cacheEntry{}}
}

func (c *resultCache) get(key string, wsID ids.WorkspaceID) (model.Response, Tier, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.now().After(entry.expires) {
		delete(c.entries, key)
		return model.Response{}, "", false
	}
	// Defense in depth for RT-AI-M7: even a corrupted key can never
	// serve another workspace's answer.
	if entry.workspaceID != wsID {
		return model.Response{}, "", false
	}
	return entry.resp, entry.tier, true
}

func (c *resultCache) put(key string, wsID ids.WorkspaceID, resp model.Response, tier Tier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{workspaceID: wsID, resp: resp, tier: tier, expires: c.now().Add(c.ttl)}
}

func (c *resultCache) invalidate(wsID ids.WorkspaceID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if entry.workspaceID == wsID {
			delete(c.entries, key)
		}
	}
}
