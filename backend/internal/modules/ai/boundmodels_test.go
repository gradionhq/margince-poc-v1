// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

func TestBoundModelIDsByProviderCoverEveryTierAndTheEmbedder(t *testing.T) {
	cfg := RoutingConfig{
		Tiers: map[Tier]ProviderConfig{
			TierLocalSmall: {Provider: "openai_compatible", Model: "vendor/small"},
			TierCheapCloud: {Provider: "openai_compatible", Model: "vendor/cheap"},
			TierPremium:    {Provider: "openai_compatible", Model: "vendor/large"},
		},
		Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: "openai_compatible", Model: "vendor/embed"}},
	}
	bound := cfg.BoundModelIDsByProvider()
	for _, want := range []string{"vendor/small", "vendor/cheap", "vendor/large", "vendor/embed"} {
		if !bound["openai_compatible"][want] {
			t.Errorf("%s is bound and must be priceable; got %v", want, bound)
		}
	}
	if len(bound) != 1 || len(bound["openai_compatible"]) != 4 {
		t.Errorf("bound = %v, want the four models under their one provider", bound)
	}
}

// A flat set would let one provider's bindings decide what another provider's
// catalog is filtered to — and make "none of the bound models appear here"
// indistinguishable from "this catalog belongs to a provider that binds nothing".
func TestBoundModelIDsByProviderKeepProvidersApart(t *testing.T) {
	cfg := RoutingConfig{
		Tiers: map[Tier]ProviderConfig{
			TierCheapCloud: {Provider: "openai_compatible", Model: "vendor/cheap"},
			TierPremium:    {Provider: "gemini", Model: "gemini-3.5-flash"},
		},
		Embeddings: EmbeddingsConfig{ProviderConfig: ProviderConfig{Provider: "gemini", Model: "gemini-embedding-001"}},
	}
	bound := cfg.BoundModelIDsByProvider()
	if bound["openai_compatible"]["gemini-3.5-flash"] {
		t.Error("a gemini model must not appear under openai_compatible")
	}
	if len(bound["gemini"]) != 2 || !bound["gemini"]["gemini-embedding-001"] {
		t.Errorf("gemini binds a chat model and the embedder; got %v", bound["gemini"])
	}
}

// A deployment that binds nothing yields an EMPTY set, not nil: a caller has to
// be able to tell "nothing is bound" from "I forgot to build the set".
func TestBoundModelIDsAreEmptyNotNilWhenNothingIsBound(t *testing.T) {
	bound := RoutingConfig{}.BoundModelIDsByProvider()
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
		TierCheapCloud: {Provider: "p", Model: "vendor/same"},
		TierPremium:    {Provider: "p", Model: " vendor/same "},
	}}
	got := cfg.BoundModelIDsByProvider()
	if len(got["p"]) != 1 || !got["p"]["vendor/same"] {
		t.Errorf("BoundModelIDsByProvider = %v, want one trimmed entry", got)
	}
}
