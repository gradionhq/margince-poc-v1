// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"encoding/json"
	"testing"
)

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

func TestToolValidate(t *testing.T) {
	valid := Tool{Name: "qualify_lead", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeWrite}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed tool must validate: %v", err)
	}
	// Title is optional, so the case above proves nothing about a declared
	// one: a written label must pass on its own.
	titled := valid
	titled.Title = "Qualify a lead"
	if err := titled.Validate(); err != nil {
		t.Fatalf("a written title must validate: %v", err)
	}
	// A description is optional at this layer for the same reason: whether one
	// is REQUIRED is decided where the tool is served, not in the grammar. What
	// the grammar owes is that a written one is renderable.
	described := valid
	described.Description = "Fill in what a lead's own data already implies, and report what is still missing."
	if err := described.Validate(); err != nil {
		t.Fatalf("a written description must validate: %v", err)
	}

	cases := []struct {
		name string
		tool Tool
	}{
		{"name not a verb", Tool{Name: "Bad-Name", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"empty name", Tool{Name: "", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"empty version", Tool{Name: "ping", Version: "", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"tier not requestable", Tool{Name: "ping", Version: "1.0.0", Tier: "dynamic", RequestedScope: ScopeRead}},
		{"scope outside vocabulary", Tool{Name: "ping", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: "admin"}},
		{"missing scope", Tool{Name: "ping", Version: "1.0.0", Tier: TierAutoExecute}},
		{"blank title", Tool{Name: "ping", Title: "   ", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"framed title", Tool{Name: "ping", Title: " Ping it ", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"non-printable title", Tool{Name: "ping", Title: "Ping\tit", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"title that is not valid UTF-8", Tool{Name: "ping", Title: "Ping\xffit", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"blank description", Tool{Name: "ping", Description: "   ", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"framed description", Tool{Name: "ping", Description: " Pings it. ", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"non-printable description", Tool{Name: "ping", Description: "Pings\tit.", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"description that is not valid UTF-8", Tool{Name: "ping", Description: "Pings\xffit.", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead}},
		{"non-object input schema", Tool{Name: "ping", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead, InputSchema: json.RawMessage(`"scalar"`)}},
		{"input schema not type object", Tool{Name: "ping", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead, InputSchema: json.RawMessage(`{"type":"array"}`)}},
		{"malformed output schema", Tool{Name: "ping", Version: "1.0.0", Tier: TierAutoExecute, RequestedScope: ScopeRead, OutputSchema: json.RawMessage(`{bad`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.tool.Validate(); err == nil {
				t.Fatalf("Tool.Validate() = nil, want a rejection for %s", tc.name)
			}
		})
	}
}
