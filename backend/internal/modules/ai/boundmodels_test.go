// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

func TestBoundModelIDsCoverEveryTierAndTheEmbedder(t *testing.T) {
	cfg := RoutingConfig{
		Tiers: map[Tier]ProviderConfig{
			TierLocalSmall: {Provider: "openai_compatible", Model: "vendor/small"},
			TierCheapCloud: {Provider: "openai_compatible", Model: "vendor/cheap"},
			TierPremium:    {Provider: "openai_compatible", Model: "vendor/large"},
		},
		Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: "openai_compatible", Model: "vendor/embed"}},
	}
	bound := cfg.BoundModelIDs()
	for _, want := range []string{"vendor/small", "vendor/cheap", "vendor/large", "vendor/embed"} {
		if !bound[want] {
			t.Errorf("%s is bound and must be priceable; got %v", want, bound)
		}
	}
	if len(bound) != 4 {
		t.Errorf("bound = %v, want exactly the four bound models", bound)
	}
}

// A deployment that binds nothing yields an EMPTY set, not nil: a caller has to
// be able to tell "nothing is bound" from "I forgot to build the set".
func TestBoundModelIDsAreEmptyNotNilWhenNothingIsBound(t *testing.T) {
	bound := RoutingConfig{}.BoundModelIDs()
	if bound == nil {
		t.Fatal("BoundModelIDs returned nil; an empty set is the honest answer")
	}
	if len(bound) != 0 {
		t.Errorf("bound = %v, want empty", bound)
	}
}

// Two tiers on the same model is one model to price, not two.
func TestBoundModelIDsDeduplicateASharedModel(t *testing.T) {
	cfg := RoutingConfig{Tiers: map[Tier]ProviderConfig{
		TierCheapCloud: {Model: "vendor/same"},
		TierPremium:    {Model: " vendor/same "},
	}}
	if got := cfg.BoundModelIDs(); len(got) != 1 || !got["vendor/same"] {
		t.Errorf("BoundModelIDs = %v, want one trimmed entry", got)
	}
}
