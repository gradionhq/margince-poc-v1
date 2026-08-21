// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

// `agent_run.agent_spec` and `runner_job.agent_spec` are plain text written
// straight from AgentSpec.Name, and the handler passes the value through
// unmapped: nothing in the schema binds a run's kind to the contract's enum.
// A renamed or added scheduled agent would serialize a value outside the enum
// and the reader would get silence, with no gate having fired. These two tests
// are the only thing holding the two halves together.

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
)

// Derived from the tree on both sides rather than from a written list: a list
// records one moment's answer and then drifts, and the drift is invisible
// precisely because both halves still look tidy.
func TestEveryScheduledAgentIsAKindTheContractDeclares(t *testing.T) {
	declared := activityKindsFromContract(t)
	for _, spec := range runner.Catalog() {
		if !declared[spec.Name] {
			t.Errorf("runner.Catalog() ships %q but ActivityKind does not declare it: "+
				"a run of this agent would report no line at all. Add it to the enum in "+
				"crm.yaml and give it copy in en/de/vi, or remove the spec.", spec.Name)
		}
	}
}

func TestTheContractDeclaresNoKindNothingCanProduce(t *testing.T) {
	shipped := map[string]bool{}
	for _, spec := range runner.Catalog() {
		shipped[spec.Name] = true
	}
	for kind := range activityKindsFromContract(t) {
		if !shipped[kind] {
			t.Errorf("ActivityKind declares %q but runner.Catalog() has no such spec: "+
				"three locales carry copy for a state nothing reaches", kind)
		}
	}
}

// activityKindsFromContract reads the enum out of the authoritative OpenAPI
// document rather than the generated Go type, so a hand edit to either one is
// still measured against the contract itself. Every step of the walk fails
// loudly: a moved or renamed schema must break this test rather than silently
// compare the catalog against an empty set.
func activityKindsFromContract(t *testing.T) map[string]bool {
	t.Helper()

	const contractPath = "../../../api/crm.yaml"
	raw, err := os.ReadFile(filepath.Clean(contractPath))
	if err != nil {
		t.Fatalf("read the contract at %s: %v", contractPath, err)
	}

	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s as YAML: %v", contractPath, err)
	}

	item, ok := doc.Components.Schemas["ActivityItem"]
	if !ok {
		t.Fatalf("components.schemas.ActivityItem is absent from %s: the kind enum this "+
			"test guards has moved or been renamed, so nothing is guarding it any more", contractPath)
	}
	kind, ok := item.Properties["kind"]
	if !ok {
		t.Fatalf("components.schemas.ActivityItem.properties.kind is absent from %s: "+
			"the run kind is no longer where this gate reads it", contractPath)
	}
	if len(kind.Enum) == 0 {
		t.Fatalf("components.schemas.ActivityItem.properties.kind declares no enum in %s: "+
			"an open string accepts any spec name, which is exactly the drift this gate exists "+
			"to catch", contractPath)
	}

	declared := make(map[string]bool, len(kind.Enum))
	for _, value := range kind.Enum {
		declared[value] = true
	}
	return declared
}
