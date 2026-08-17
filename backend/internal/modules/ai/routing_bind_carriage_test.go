// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"slices"
	"testing"
)

// The trap `input:` sets for an operator, held here rather than only in the
// docs. A task's carriage is the INTERSECTION over its bound rungs, because the
// budget guardrail can demote a call mid-month — so declaring the modality on
// the tier you were thinking of buys nothing while a sibling rung on the same
// ladder stays undeclared. Discovering that from a refused document, after
// editing the config and restarting, is the experience this test exists to
// prevent someone from shipping.
func TestDeclaringInputOnOneRungOfATwoRungLadderCarriesNothing(t *testing.T) {
	// rate_extract's ladder is {premium, cheap_cloud} — two rungs, so both must
	// agree before a caller may be told a document can go to this task.
	const twoRung = TaskRateExtract
	if len(TaskLadder(twoRung)) != 2 {
		t.Fatalf("this test needs a two-rung ladder; %s has %v", twoRung, TaskLadder(twoRung))
	}

	routing := func(cheapInput []string) RoutingConfig {
		return RoutingConfig{
			Profile: ProfileCloudFrontier,
			Tiers: map[Tier]ProviderConfig{
				TierPremium:    {Provider: providerOpenAICompatible, BaseURL: "https://x", Model: "m", Input: []string{"text", "image"}},
				TierCheapCloud: {Provider: providerOpenAICompatible, BaseURL: "https://x", Model: "c", Input: cheapInput},
			},
			Embeddings: EmbeddingsConfig{
				ProviderConfig: ProviderConfig{Provider: providerOpenAICompatible, BaseURL: "https://x", Model: "e"},
				Dimensions:     defaultEmbedDimensions,
			},
		}.WithKeys(allCloudKeys())
	}

	t.Run("one rung declared is not enough", func(t *testing.T) {
		router, err := NewRouter(routing(nil), nil, nil, nil, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := router.AttachmentMIMEs(twoRung); len(got) != 0 {
			t.Fatalf("an undeclared sibling rung must veto the lane, got %v", got)
		}
	})

	t.Run("both rungs declared carries the modality", func(t *testing.T) {
		router, err := NewRouter(routing([]string{"text", "image"}), nil, nil, nil, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := router.AttachmentMIMEs(twoRung); !slices.Equal(got, []string{"image/*"}) {
			t.Fatalf("both rungs declaring image must carry it, got %v", got)
		}
	})
}
