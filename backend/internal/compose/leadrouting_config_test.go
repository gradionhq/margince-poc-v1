// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose_test

// The assign_lead_owner config seam, fixture-tested end to end (B-E13.7b
// reusable-artifact DoD): the catalog's params_schema in automation and the
// RoutingConfig decoder in people describe the SAME shape — a fixture
// the validator accepts must decode losslessly, an out-of-schema knob
// must be refused, and the schema's property names must be exactly the
// knobs the decoder reads, so the two sides cannot drift apart.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestLeadRoutingConfigValidatesAndDecodesFromOneFixture(t *testing.T) {
	entry, ok := automation.CatalogEntryByKey("assign_lead_owner")
	if !ok {
		t.Fatal("assign_lead_owner left the closed catalog")
	}
	if entry.Trigger != "lead.created" || entry.Action != "assign_owner" || entry.Tier != "auto_execute" {
		t.Fatalf("assign_lead_owner entry drifted: trigger=%s action=%s tier=%s", entry.Trigger, entry.Action, entry.Tier)
	}

	poolA, poolB, ruleOwner := ids.New[ids.UserKind](), ids.New[ids.UserKind](), ids.New[ids.UserKind]()
	fixture := map[string]any{
		"owners":        []any{poolA.String(), poolB.String()},
		"cap_per_owner": float64(5),
		"rules": []any{
			map[string]any{"field": "source", "equals": "webinar", "owner_id": ruleOwner.String()},
		},
	}
	if err := entry.Validate(fixture); err != nil {
		t.Fatalf("the canonical fixture fails catalog validation: %v", err)
	}

	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := people.ParseRoutingConfig(raw)
	if err != nil {
		t.Fatalf("the validated fixture fails the runtime decode: %v", err)
	}
	if len(cfg.Owners) != 2 || cfg.Owners[0] != poolA || cfg.Owners[1] != poolB {
		t.Fatalf("pool order lost in decode: %+v", cfg.Owners)
	}
	if cfg.CapPerOwner != 5 {
		t.Fatalf("cap decoded as %d, want 5", cfg.CapPerOwner)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Field != "source" || cfg.Rules[0].Equals != "webinar" || cfg.Rules[0].OwnerID != ruleOwner {
		t.Fatalf("rule lost in decode: %+v", cfg.Rules)
	}

	// The anti-DSL guard: a knob outside the schema never reaches the
	// table, so the decoder never meets it.
	rogue := map[string]any{"rule_body": "if source == x then owner = y"}
	if err := entry.Validate(rogue); err == nil {
		t.Fatal("an out-of-schema knob must be refused at the catalog gate")
	}
	for _, bad := range []map[string]any{
		{"owners": "not-a-list"},
		{"cap_per_owner": float64(0)},
		{"rules": []any{map[string]any{"field": "relationship_strength", "equals": "x", "owner_id": poolA.String()}}},
		{"rules": []any{map[string]any{"field": "source", "equals": "x"}}},
	} {
		if err := entry.Validate(bad); err == nil {
			t.Fatalf("invalid params %v passed catalog validation", bad)
		}
	}

	// The schema's properties are exactly the decoder's knobs: a knob
	// added on one side without the other fails here.
	properties, ok := entry.ParamsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("assign_lead_owner params_schema carries no properties: %v", entry.ParamsSchema)
	}
	decoderKnobs := map[string]bool{"owners": true, "cap_per_owner": true, "rules": true}
	if len(properties) != len(decoderKnobs) {
		t.Fatalf("schema has %d properties, decoder reads %d", len(properties), len(decoderKnobs))
	}
	for name := range properties {
		if !decoderKnobs[name] {
			t.Fatalf("schema property %q has no decoder counterpart", name)
		}
	}
}
