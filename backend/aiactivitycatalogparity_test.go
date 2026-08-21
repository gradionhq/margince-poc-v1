// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The AI-activity contract must name exactly the work that can reach it, and
// cap exactly what the read caps.
//
// Both halves were held by a gate inside the package the read used to live in.
// That package is gone, and the obligations are not: a spec whose name the
// contract does not carry announces a kind the wire cannot express and the rail
// renders NOTHING for it — silently, with the agent really running. Restored
// here because the root is the only place that can see the catalog, the
// contract and the read's own bounds at once.

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
)

// documentReadingKind is the one kind that is not a scheduled spec: a human
// asking for an attached document to be read. Named here rather than derived
// because the emitter is a plain constant in another module, and this gate's
// job is to notice when the two sets stop agreeing.
const documentReadingKind = "document_extract"

func TestEveryScheduledSpecIsAKindTheContractCanExpress(t *testing.T) {
	declared := crmYAMLEnum(t, "AiActivityItem", "kind")
	if len(declared) == 0 {
		t.Fatal("AiActivityItem declares no kind enum; this gate would pass vacuously")
	}
	for _, spec := range runner.Catalog() {
		if !slices.Contains(declared, spec.Name) {
			t.Errorf("runner.Catalog() ships %q and the contract's kind enum does not carry it — the "+
				"runner would announce a kind the wire cannot express, and the rail would render nothing "+
				"for an agent that really ran. Add it to the enum and ship its copy in en/de/vi", spec.Name)
		}
	}
}

// The reverse: a kind nothing can produce is copy three locales carry, a line
// no reader will see, and a promise the server cannot keep.
func TestEveryContractKindHasSomethingThatProducesIt(t *testing.T) {
	produced := []string{documentReadingKind}
	for _, spec := range runner.Catalog() {
		produced = append(produced, spec.Name)
	}
	for _, kind := range crmYAMLEnum(t, "AiActivityItem", "kind") {
		if !slices.Contains(produced, kind) {
			t.Errorf("the contract declares kind %q and nothing announces it — either an emitter was "+
				"removed and the enum kept its name, or the name is aspirational. Drop it, or point this "+
				"gate at what produces it", kind)
		}
	}
}

// The read caps two free-text columns on the way to the wire, and the contract
// publishes those caps as maxLength. A cap larger than the published one ships
// a string a strict client rejects; a smaller one truncates below what the
// contract promised a reader would get.
func TestTheReadsTextCapsAreTheOnesTheContractPublishes(t *testing.T) {
	for _, b := range []struct {
		property string
		cap      int
	}{
		{"summary", aiactivity.SummaryBound},
		{"degrade_reason", aiactivity.DegradeReasonBound},
	} {
		if got := crmYAMLMaxLength(t, "AiActivityItem", b.property); got != b.cap {
			t.Errorf("the read caps %s at %d but the contract publishes maxLength %d", b.property, b.cap, got)
		}
	}
}

// crmYAMLMaxLength reads one property's maxLength out of the contract.
func crmYAMLMaxLength(t *testing.T, schema, property string) int {
	t.Helper()
	// Decoded loosely rather than into a tagged struct: `maxLength` is
	// OpenAPI's spelling, and a yaml tag naming it fails the repo's snake_case
	// tag rule — a rule about OUR wire shapes, which this is not.
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	raw, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	prop, ok := doc.Components.Schemas[schema].Properties[property]
	if !ok {
		t.Fatalf("%s declares no %s property", schema, property)
	}
	published, ok := prop["maxLength"].(int)
	if !ok {
		t.Fatalf("%s.%s publishes no maxLength, so the read's cap is unheld", schema, property)
	}
	return published
}

// A spec name that is not a legal message-key segment cannot have copy keyed on
// it, which is the other half of "the rail renders nothing".
func TestEverySpecNameCanBeAMessageKeySegment(t *testing.T) {
	for _, spec := range runner.Catalog() {
		if strings.ContainsAny(spec.Name, " .") || spec.Name == "" {
			t.Errorf("spec name %q cannot key a locale message", spec.Name)
		}
	}
}
