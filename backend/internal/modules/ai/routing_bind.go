// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

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
