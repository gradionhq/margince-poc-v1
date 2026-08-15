// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "slices"

// ModelRef is a bound (provider, model) pair — the identity a rate is keyed
// on. The cost pre-flight estimator prices an observed served slice against
// one of these.
type ModelRef struct{ Provider, Model string }

// embedInclusiveMeta builds the tier→binding map every Router carries. The
// completion tiers come straight from cfg.Tiers; the embed lane is folded in
// under TierEmbedLane from cfg.Embeddings — it is not a completion tier (it is
// absent from knownTiers/cfg.Tiers), yet its identity must be resolvable so the
// embed trace can stamp its provider/model (Router.Embed) and the cost
// estimator can price embeddings against the model that runs them.
func embedInclusiveMeta(cfg RoutingConfig) map[Tier]routeMeta {
	meta := make(map[Tier]routeMeta, len(cfg.Tiers)+1)
	for tier, binding := range cfg.Tiers {
		meta[tier] = routeMeta{provider: binding.Provider, model: binding.Model}
	}
	if cfg.Embeddings.Model != "" {
		meta[TierEmbedLane] = routeMeta{provider: cfg.Embeddings.Provider, model: cfg.Embeddings.Model}
	}
	return meta
}

// BoundLadder returns task's currently-bound rungs in ladder order, skipping
// any tier the routing config leaves unbound. The result is empty when nothing
// is bound — a caller MUST treat that as "unpriced" and never index [0]
// blindly.
//
// A pre-flight estimate answers "what does the standing configuration charge",
// so this reads the bindings as configured and deliberately does NOT apply the
// transient budget/economy degradation the live router bends an individual
// call through — otherwise the same preview would quote different figures as
// the month's spend drifted across budget bands.
func (r *Router) BoundLadder(task Task) []ModelRef {
	tiers := taskLadders[task]
	if task == TaskEmbeddings {
		// Embeddings run the embed lane, which is not a completion tier and so
		// is absent from taskLadders; its binding lives under TierEmbedLane.
		tiers = []Tier{TierEmbedLane}
	}
	ladder := make([]ModelRef, 0, len(tiers))
	for _, tier := range tiers {
		if ref, ok := r.CurrentModelForTier(tier); ok {
			ladder = append(ladder, ref)
		}
	}
	return ladder
}

// AttachmentMIMEs reports what a caller may hand task as a document part: the
// media types EVERY bound rung of its ladder declares it carries. Empty means
// this task cannot be given a document at all under the standing configuration.
//
// The INTERSECTION, not the union, and not the leading rung's set alone. A call
// walks its ladder, and the budget guardrail can demote it to a lower rung
// mid-month, so a caller that trusted the top rung's carriage would be right
// until the month it wasn't — and would then fail on the one call it had
// already decided was safe. The conservative set is the only one that stays
// true for a call this router might serve on any rung.
//
// An unbound tier contributes nothing: it is skipped exactly as BoundLadder
// skips it, because a rung nothing is bound to cannot serve the call either.
func (r *Router) AttachmentMIMEs(task Task) []string {
	var carried []string
	// An explicit flag, not "is carried still nil": a first rung that declares
	// NOTHING is the case the nil test gets wrong. It leaves carried nil, the
	// next rung is then taken as the starting set rather than intersected with
	// an empty one, and the task advertises carriage its leading rung refuses.
	var started bool
	for _, tier := range taskLadders[task] {
		client, bound := r.clients[tier]
		if !bound {
			continue
		}
		declared := client.Caps().AttachmentMIMEs
		if !started {
			carried, started = declared, true
			continue
		}
		carried = intersectMIMEs(carried, declared)
	}
	return carried
}

// intersectMIMEs keeps the declarations both rungs make, compared as the
// literal patterns they are. Two rungs spelling the same carriage differently
// ("image/*" against "image/png") intersect to nothing, which is the safe
// direction: it costs a document lane that might have worked, where the other
// direction would promise carriage one rung cannot honour.
func intersectMIMEs(a, b []string) []string {
	kept := make([]string, 0, len(a))
	for _, pattern := range a {
		if slices.Contains(b, pattern) {
			kept = append(kept, pattern)
		}
	}
	return kept
}

// CurrentModelForTier returns the model currently bound to tier; ok=false when
// that tier is unbound (no routeMeta entry, or an entry whose model is empty).
// This is the reprice target for a served slice whose own model has since
// departed the ladder — keyed on the slice's recorded ai_call.tier.
func (r *Router) CurrentModelForTier(tier Tier) (ModelRef, bool) {
	m, ok := r.routeMeta[tier]
	// An empty model id counts as unbound DELIBERATELY: the offline `fake` provider
	// legitimately binds {provider: fake} with no model id, and there is no model
	// rate to price such a lane against — so it degrades to the estimator's
	// floor/unpriced path, which is the acceptable dev/cert posture. Treating a
	// genuinely-unpriceable binding as "bound" would be worse: it would quote a
	// confident cost the deployment cannot actually charge. BoundLadder inherits
	// this — an all-fake ladder resolves empty, i.e. unpriced.
	if !ok || m.model == "" {
		return ModelRef{}, false
	}
	return ModelRef{Provider: m.provider, Model: m.model}, true
}
