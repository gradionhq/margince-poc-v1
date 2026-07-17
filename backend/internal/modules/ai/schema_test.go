// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// The editor schema's enums must equal the parser's authorities, or the schema
// silently lies to operators (autocompletes a provider the parser rejects, or
// omits one it accepts). Adding a provider without touching the schema fails here.
func TestRoutingSchemaEnumsMatchCode(t *testing.T) {
	raw, err := os.ReadFile("../../../../config/ai-routing.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema struct {
		Properties struct {
			Profile struct{ Enum []string } `json:"profile"`
			Tiers   struct {
				//nolint:tagliatelle // "propertyNames" is JSON Schema's own keyword, camelCase by spec
				PropertyNames struct{ Enum []string } `json:"propertyNames"`
			} `json:"tiers"`
		} `json:"properties"`
		Defs struct {
			Binding struct {
				Properties struct {
					Provider struct{ Enum []string } `json:"provider"`
				} `json:"properties"`
			} `json:"binding"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	assertSetEqual(t, "profiles", schema.Properties.Profile.Enum,
		[]string{string(ProfileEUHosted), string(ProfileSovereign), string(ProfileCloudFrontier)})
	tierNames := make([]string, 0, len(knownTiers))
	for tier := range knownTiers {
		tierNames = append(tierNames, string(tier))
	}
	assertSetEqual(t, "tiers", schema.Properties.Tiers.PropertyNames.Enum, tierNames)
	assertSetEqual(t, "providers", schema.Defs.Binding.Properties.Provider.Enum, knownProviders)
}

func assertSetEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("%s: schema %v != code %v", label, g, w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("%s: schema %v != code %v", label, g, w)
		}
	}
}
