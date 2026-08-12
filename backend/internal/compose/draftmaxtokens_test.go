// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Every drafting request carries thinking headroom.
//
// A reasoning model spends output tokens on internal thinking BEFORE its
// answer, and that thinking counts against the cap. A request that sets none
// takes the provider's default, and on a premium rung the answer is starved
// into a MAX_TOKENS stop with zero visible text — which is exactly how an
// attempt to raise the drafting tier failed, on the two sites that had never
// set it while the reply site always had.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestEveryDraftingRequestCarriesThinkingHeadroom(t *testing.T) {
	person, err := persondraft.GroundedRequest(persondraft.Input{
		Recipient: persondraft.RecipientIn{ID: "p1", Name: "Marek", FirstName: "Marek"},
	})
	if err != nil {
		t.Fatal(err)
	}
	account, err := accountdraft.GroundedRequest(accountdraft.Input{
		Company:   "Northwind",
		Recipient: accountdraft.RecipientIn{ID: "p1", Name: "Priya"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]int{
		"person":  person.MaxTokens,
		"account": account.MaxTokens,
	} {
		if got != ai.ReasoningOutputMaxTokens {
			t.Errorf("the %s drafting request caps output at %d, want %d — a request "+
				"with no headroom is starved into MAX_TOKENS on a reasoning model",
				name, got, ai.ReasoningOutputMaxTokens)
		}
	}
}
