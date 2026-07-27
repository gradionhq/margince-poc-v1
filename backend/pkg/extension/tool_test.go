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
