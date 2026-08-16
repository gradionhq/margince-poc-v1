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
					Input    struct {
						Items struct{ Enum []string } `json:"items"`
					} `json:"input"`
				} `json:"properties"`
				AllOf []struct {
					If struct {
						Properties struct {
							Provider struct{ Enum []string } `json:"provider"`
						} `json:"properties"`
					} `json:"if"`
					Else map[string]any `json:"else"`
				} `json:"allOf"` //nolint:tagliatelle // "allOf" is JSON Schema's own keyword, camelCase by spec
			} `json:"binding"`
			//nolint:tagliatelle // "$defs" member names are this schema's own, matching the file
			EmbeddingsBinding struct {
				Properties map[string]any `json:"properties"`
			} `json:"embeddingsBinding"`
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
	assertSetEqual(t, "input modalities", schema.Defs.Binding.Properties.Input.Items.Enum, acceptedModalities)

	// `input:` is accepted on every provider — as the whole answer on the
	// OpenAI-compatible wire, as a narrowing everywhere else — so the schema must
	// not carry the conditional that once forbade it outside those two. A schema
	// stricter than the parser is worse than no schema: it makes an editor red
	// on a config that boots.
	for _, clause := range schema.Defs.Binding.AllOf {
		if clause.Else != nil {
			t.Errorf("the binding $def still forbids `input` on some providers: %v", clause.If.Properties.Provider.Enum)
		}
	}

	// The embeddings lane sends no attachments, so its own $def must not offer
	// the field at all. (The parser is what enforces this — EmbeddingsConfig
	// embeds ProviderConfig inline, so yaml decodes `input:` there regardless —
	// but a schema that offered it would still invite the mistake.)
	if _, offered := schema.Defs.EmbeddingsBinding.Properties["input"]; offered {
		t.Error("embeddingsBinding must not offer `input`")
	}
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
