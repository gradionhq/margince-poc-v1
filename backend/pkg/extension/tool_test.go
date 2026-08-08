// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import "testing"

func TestTierValidate(t *testing.T) {
	for _, valid := range []Tier{TierAutoExecute, TierConfirmationRequired} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Tier(%q).Validate() = %v, want nil", valid, err)
		}
	}
	// dynamic needs a resolver (behavior) and is not requestable through
	// the static declaration; the empty and unknown values are rejected too.
	for _, invalid := range []Tier{"dynamic", "", "purple"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Tier(%q).Validate() = nil, want the rejection", invalid)
		}
	}
}

func TestScopeValidate(t *testing.T) {
	for _, valid := range []Scope{ScopeRead, ScopeDraft, ScopeWrite, ScopeSend, ScopeEnrich} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Scope(%q).Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Scope{"", "admin", "READ"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Scope(%q).Validate() = nil, want the rejection", invalid)
		}
	}
}

// TestToolValidate: after the narrowing a Tool is {Name, Handle}, so the only
// rule left is the verb grammar. Everything the old cases here covered — the
// tier and scope vocabularies, the renderable title and description, the schema
// shapes — moved to the declaration, and moved with it to TestVerbValidate.
func TestToolValidate(t *testing.T) {
	if err := (Tool{Name: "qualify_lead"}).Validate(); err != nil {
		t.Fatalf("a well-formed tool must validate: %v", err)
	}
	// A nil Handle is legal at this layer: it is how "declare it, serve
	// nothing" is spelled, and whether a verb serves is decided where it is
	// registered, not in the grammar.
	if err := (Tool{Name: "qualify_lead", Handle: nil}).Validate(); err != nil {
		t.Fatalf("an inert tool must validate: %v", err)
	}
	for _, name := range []string{"Bad-Name", "", "two words", "_leading", "trailing_"} {
		if err := (Tool{Name: name}).Validate(); err == nil {
			t.Errorf("Tool{Name: %q}.Validate() = nil, want the verb rejection", name)
		}
	}
}
