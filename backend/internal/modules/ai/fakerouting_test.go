// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// FakeRoutingConfig must bind every ladder or --ai-fake would silently
// refuse whichever task happened to land on the gap (TaskSiteExtract's
// ladder is premium-only, so a config that skipped the premium tier
// would look fine for every other task and only fail deep-read
// extraction). UnboundLadderWarnings is the same completeness check the
// boot path runs over a declared ai-routing.yaml, so reusing it here
// keeps the fake config honest against the SAME contract, not a
// hand-maintained parallel list of tasks.
func TestFakeRoutingConfigBindsEveryLadder(t *testing.T) {
	cfg := FakeRoutingConfig()
	if err := cfg.validate(); err != nil {
		t.Fatalf("FakeRoutingConfig() failed its own validation: %v", err)
	}
	if got := cfg.UnboundLadderWarnings(); len(got) != 0 {
		t.Fatalf("FakeRoutingConfig() leaves ladders unbound: %v", got)
	}
	if cfg.Profile != ProfileEUHosted {
		t.Fatalf("Profile = %q, want %q", cfg.Profile, ProfileEUHosted)
	}
	for _, tier := range []Tier{TierLocalSmall, TierCheapCloud, TierPremium} {
		if got := cfg.Tiers[tier].Provider; got != ProviderFake {
			t.Fatalf("tier %s provider = %q, want %q", tier, got, ProviderFake)
		}
	}
	if got := cfg.Embeddings.Provider; got != ProviderFake {
		t.Fatalf("embeddings provider = %q, want %q", got, ProviderFake)
	}
}
