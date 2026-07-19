// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// FakeRoutingConfig is the routing config the offline --ai-fake dev/test
// path is built over. It binds EVERY tier the contract knows about
// (local_small, cheap_cloud, premium) plus the embeddings lane to
// ProviderFake, so every task's fallback ladder is fully bound —
// UnboundLadderWarnings reports nothing for it, and no ladder is ever
// refused because --ai-fake happened to leave a tier unbound. That
// matters because TaskSiteExtract's ladder is premium-only by contract
// (no fallback rung): a fake config that skipped the premium tier would
// silently refuse deep-read extraction under --ai-fake while every
// other lane worked, an inconsistency a dev/test flag must never
// produce. Riding this config through NewModelPath (rather than
// FakeModelPath's direct client wiring) means --ai-fake exercises the
// real Router — tiering, the budget guardrail, metering and call
// tracing — with only the provider swapped for the deterministic fake.
func FakeRoutingConfig() RoutingConfig {
	fake := ProviderConfig{Provider: ProviderFake}
	return RoutingConfig{
		Profile: ProfileEUHosted,
		Tiers: map[Tier]ProviderConfig{
			TierLocalSmall: fake,
			TierCheapCloud: fake,
			TierPremium:    fake,
		},
		Embeddings: fake,
	}
}
