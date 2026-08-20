// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The wire mapping, which is where a binding loses a field silently. A dropped
// base_url or input list does not fail anything — it routes to a different
// endpoint, or refuses a document the operator bound a model to carry.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestABindingSurvivesTheRoundTripToTheWireAndBack(t *testing.T) {
	original := ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierPremium: {
				Provider: "gemini", Model: "gemini-3.5-flash",
				BaseURL: "https://eu-gateway.example", Input: []string{"text", "image"},
			},
			ai.TierCheapCloud: {Provider: "gemini", Model: "gemini-3.1-flash-lite"},
		},
		Embeddings: ai.EmbeddingsConfig{
			ProviderConfig: ai.ProviderConfig{Provider: "gemini", Model: "gemini-embedding-001"},
			Dimensions:     1536,
		},
	}

	back := fromContractAiRouting(toContractAiRouting(original))

	if back.Profile != original.Profile {
		t.Errorf("profile = %q, want %q", back.Profile, original.Profile)
	}
	if len(back.Tiers) != len(original.Tiers) {
		t.Fatalf("tiers = %v, want %d of them", back.Tiers, len(original.Tiers))
	}
	for tier, want := range original.Tiers {
		got := back.Tiers[tier]
		if got.Provider != want.Provider || got.Model != want.Model || got.BaseURL != want.BaseURL {
			t.Errorf("tier %s = %+v, want %+v", tier, got, want)
		}
		if len(got.Input) != len(want.Input) {
			t.Errorf("tier %s input = %v, want %v — a narrowed carriage that vanishes silently widens what the model may be sent", tier, got.Input, want.Input)
		}
	}
	if back.Embeddings.Dimensions != original.Embeddings.Dimensions {
		t.Errorf("embeddings width = %d, want %d", back.Embeddings.Dimensions, original.Embeddings.Dimensions)
	}
}

// Absent and empty are different to an operator and the same to a Go zero
// value. "No base_url override" must not read back as "base_url set to the
// empty string", which is why these fields are pointers on the wire.
func TestAnUnsetOptionalIsAbsentRatherThanEmpty(t *testing.T) {
	wire := toContractAiRouting(ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers:   map[ai.Tier]ai.ProviderConfig{ai.TierPremium: {Provider: "fake", Model: "m"}},
	})
	tier := wire.Tiers["premium"]
	if tier.BaseUrl != nil {
		t.Errorf("base_url = %q, want absent", *tier.BaseUrl)
	}
	if tier.Input != nil {
		t.Errorf("input = %v, want absent", *tier.Input)
	}
	// Reported as STORED, not as defaulted: a GET → PUT round-trip must not
	// freeze today's compiled default into the document as though an operator
	// had chosen it, because then tomorrow's default would not reach them.
	if wire.Embeddings.Dimensions != nil {
		t.Errorf("dimensions = %d, want absent for an unset width", *wire.Embeddings.Dimensions)
	}
}

// An unbound installation answers `{}`, never null. A null leaves a client
// unable to tell "nothing is bound" from "the field was omitted".
func TestAnUnboundInstallationReportsAnEmptyTierMapNotNull(t *testing.T) {
	wire := toContractAiRouting(ai.RoutingConfig{})
	if wire.Tiers == nil {
		t.Error("tiers is null; an unbound installation must say so with an empty object")
	}
	if len(wire.Tiers) != 0 {
		t.Errorf("tiers = %v, want empty", wire.Tiers)
	}
}

// A submitted document with no tiers maps to the unconfigured config, which is
// what lets an operator unbind every model deliberately.
func TestASubmittedDocumentWithNoTiersIsUnconfigured(t *testing.T) {
	cfg := fromContractAiRouting(crmcontracts.AiRouting{Profile: "eu_hosted"})
	if !cfg.Unconfigured() {
		t.Errorf("tiers = %v, want unconfigured", cfg.Tiers)
	}
}
