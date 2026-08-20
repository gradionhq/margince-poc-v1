// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// What a Router serves, and how that is replaced without restarting.
//
// Split from router.go because it answers a different question. router.go is
// how a call is routed; this is what it is routed AGAINST, and the two have
// different lifetimes now that the second can change under the first.

import (
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// binding is everything about a Router that comes from the routing config: the
// set that must change together or not at all.
//
// It is separate from the Router because it is REPLACEABLE. Everything left on
// the Router — the meter, the budget, the call store, the metrics collector —
// is this process's, and survives a change of which models are bound; a rebind
// that reset those would lose a workspace's spend against its budget and
// restart the process-wide counters /metrics reports.
//
// Held immutably behind an atomic pointer, and never mutated in place. A call
// loads it ONCE and serves itself from that snapshot, so it cannot mix a tier
// resolved under the old binding with the config hash of the new one — which
// would file the call under a configuration that never served it.
type binding struct {
	clients        map[Tier]model.Client
	embedder       model.Client
	profile        Profile
	routeMeta      map[Tier]routeMeta
	configSnapshot ConfigSnapshot
	// configHash is the ai_call_config dimension key. Empty on a Router
	// assembled without a RoutingConfig (most unit tests via assembleRouter),
	// which makes flush skip EnsureConfig and leave every attempt's ConfigHash
	// nil — the behaviour that predates this struct.
	configHash string
	// embedDims is the configured embeddings width. Zero on a Router assembled
	// without a RoutingConfig: Embed then leaves a caller-unset Dimensions at
	// 0 and each adapter's own provider default still applies.
	embedDims int
}

// binding returns the configuration this call must serve itself from. Load it
// once and pass the value down: a second load mid-call can straddle a rebind.
func (r *Router) binding() *binding {
	if b := r.bound.Load(); b != nil {
		return b
	}
	// A Router assembled without a RoutingConfig (assembleRouter directly, most
	// unit tests) has no binding. The zero value reproduces exactly what those
	// tests saw before the field existed rather than nil-panicking on them.
	return &binding{}
}

// withConfigSnapshot returns the binding with its ai_call_config dimension row
// and embed width stamped on — the one place that keeps both in sync, since the
// snapshot's provider_params must name the SAME width Embed defaults an unset
// request to. Pure: EnsureConfig plants the row lazily, once per flush.
func (b binding) withConfigSnapshot(routingConfigHash string, embedDims int) binding {
	// ParseRouting defaults 0→defaultEmbedDimensions, but a programmatic
	// RoutingConfig built without it (a hand-assembled test fixture) reaches
	// construction with Dimensions still 0 — default here too so a bound embed
	// lane never stamps its identity as "@0" or asks a provider for width 0.
	if embedDims == 0 {
		embedDims = defaultEmbedDimensions
	}
	b.embedDims = embedDims
	b.configSnapshot = newConfigSnapshot(routingConfigHash, embedDims)
	b.configHash = b.configSnapshot.Hash
	return b
}

// Rebind swaps which models this Router serves, without restarting the
// process. Everything not derived from the routing config — the meter, the
// budget, the call store, the process-wide metrics — is kept, so a rebind
// loses neither a workspace's spend against its budget nor the counters
// /metrics reports.
//
// The result cache is dropped, and that is not housekeeping: every entry in it
// was produced by the binding being replaced, and serving one afterwards would
// attribute a previous model's words to the one now bound. It is the same
// reason the routing version is folded into the brief fingerprints.
//
// A call already in flight finishes on the binding it loaded. That is the
// correct outcome rather than a compromise: a call is atomic in its own
// configuration, so it lands in the meter under the config that actually
// served it.
func (r *Router) Rebind(cfg RoutingConfig) error {
	clients, embedder, err := cfg.buildClients()
	if err != nil {
		return err
	}
	next := binding{
		clients: clients, embedder: embedder,
		profile: cfg.Profile, routeMeta: embedInclusiveMeta(cfg),
	}.withConfigSnapshot(cfg.sourceHash, cfg.Embeddings.Dimensions)
	r.bound.Store(&next)
	r.cache.clear()
	return nil
}

// RoutingVersion is the digest of the ROUTING CONFIG this Router is serving —
// the value a caller compares against the stored one to decide whether it has
// fallen behind. Empty on a Router assembled without a RoutingConfig, which is
// the same "no deployment binding to name" a parsed config reports.
//
// Deliberately NOT configHash, which is the ai_call_config dimension key and
// folds in the task-contract hash and provider params as well. The two are
// different values, and comparing a stored routing digest against a snapshot
// hash never matches — so a caller polling for change would rebind on every
// tick and drop every cached completion each time.
func (r *Router) RoutingVersion() string { return r.binding().configSnapshot.RoutingConfigHash }
