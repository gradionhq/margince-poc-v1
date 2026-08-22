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
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
)

// documentReadingKind is the one kind that is not a scheduled spec: a human
// asking for an attached document to be read.
//
// Read from the EMITTER's own exported constant rather than restated, so a
// carrier that renames its kind fails here instead of leaving this gate
// vouching for a name nothing writes. Its ai_task and its display kind are one
// string for this carrier — the reading IS the task — which is why one constant
// answers both.
const documentReadingKind = activities.ExtractionAITask

func TestEveryKindSomethingProducesIsOneTheContractCanExpress(t *testing.T) {
	declared := crmYAMLNamedEnum(t, "AiActivityKind")
	if len(declared) == 0 {
		t.Fatal("AiActivityKind declares no enum; this gate would pass vacuously")
	}
	var missing []string
	for _, kind := range producedKinds() {
		if !slices.Contains(declared, kind) {
			missing = append(missing, kind)
			t.Errorf("something announces kind %q and the contract's enum does not carry it — the wire "+
				"cannot express it, and the rail would render nothing for AI work that really happened. "+
				"Add it to the enum and ship its copy in en/de/vi", kind)
		}
	}
	if len(missing) > 0 {
		t.Log(alignEnum(missing))
	}
}

// alignEnum names the file, the schema and exactly what to add.
//
// It deliberately does NOT render a replacement enum, and the reason is worth
// keeping: crmYAMLNamedEnum SORTS what it reads, because its callers do set
// comparisons. So no helper built on it can reproduce the contract's own order
// — and that order is deliberate, opening with the three carrier kinds the
// schema's own description explains. A "paste this" block built from a sorted
// list would move every name in the enum in order to add one: a diff nobody can
// review, and a grouping silently destroyed.
//
// Naming the file and the missing names is the part that was actually missing.
// The per-kind errors above already say why each one matters.
func alignEnum(missing []string) string {
	out := append([]string{}, missing...)
	sort.Strings(out)
	return fmt.Sprintf(
		"align: backend/api/crm.yaml — add %s to the AiActivityKind enum, keeping its existing order "+
			"(the carrier kinds lead it on purpose), then ship each one's copy in en/de/vi",
		strings.Join(out, ", "))
}

// producedKinds is every kind an emitter can announce.
//
// Three producers, and the third is why this function is derived rather than
// listed: the ROUTER announces on behalf of every task the rail registry leaves
// to it, under the task's own name. That set grows the moment somebody declares
// a task in api/ai-tasks.yaml, so a list here would be one edit behind the
// contract forever — which is the shape of the defect that left seventeen
// shipped tasks reporting nothing at all.
//
// Both directions of this parity are checked against THIS list, which is why it
// is one function rather than two inline slices — a producer named in only one
// direction is a producer half-gated, and the half that is missing is whichever
// one nobody thought about.
func producedKinds() []string {
	out := []string{documentReadingKind}
	for _, spec := range runner.Catalog() {
		out = append(out, spec.Name)
	}
	for task, source := range ai.RailOwners() {
		if source == ai.SourceRouter {
			out = append(out, task)
		}
	}
	return out
}

// The reverse: a kind nothing can produce is copy three locales carry, a line
// no reader will see, and a promise the server cannot keep.
func TestEveryContractKindHasSomethingThatProducesIt(t *testing.T) {
	produced := producedKinds()
	for _, kind := range crmYAMLNamedEnum(t, "AiActivityKind") {
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
	// `maxLength` is OpenAPI's own spelling, so the tag cannot be snake_case:
	// the repo's tag rule is about the shapes WE publish, and this decodes a
	// document whose key names are not ours to choose. Typed rather than a bare
	// map so an absent field stays a distinguishable nil.
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					MaxLength *int `yaml:"maxLength"` //nolint:tagliatelle // OpenAPI's key, not ours to rename
				} `yaml:"properties"`
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
	if !ok || prop.MaxLength == nil {
		t.Fatalf("%s.%s publishes no maxLength, so the read's cap is unheld", schema, property)
	}
	return *prop.MaxLength
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
