// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

// `agent_run.agent_spec` and `runner_job.agent_spec` are plain text written
// straight from AgentSpec.Name, and the handler passes the value through
// unmapped: nothing in the schema binds a run's kind to the contract's enum.
// A renamed or added scheduled agent would serialize a value outside the enum
// and the reader would get silence, with no gate having fired. The kind tests
// below are the only thing holding the two halves together.
//
// The same file also binds the two free-text bounds: the contract publishes them
// as maxLength, so a client sizes a column against them, and a Go constant that
// drifted from the published number would truncate somewhere no reader was told
// about.

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
			t.Errorf("ActivityItem.kind declares %q but runner.Catalog() ships no such agent, so "+
				"three locales carry copy for work nothing can produce. Remove %[1]q from the enum in "+
				"crm.yaml and delete its agent.activity.* lines from en/de/vi, or ship the spec that "+
				"produces it.", kind)
		}
	}
}

// contractProperty is the part of one ActivityItem property this file measures
// against the Go side.
type contractProperty struct {
	Enum []string `yaml:"enum"`
	//nolint:tagliatelle // OpenAPI names this key, not us.
	MaxLength *int `yaml:"maxLength"`
}

// activityKindsFromContract reads the enum out of the authoritative OpenAPI
// document rather than the generated Go type, so a hand edit to either one is
// still measured against the contract itself.
func activityKindsFromContract(t *testing.T) map[string]bool {
	t.Helper()
	kind := activityItemProperty(t, "kind")
	if len(kind.Enum) == 0 {
		t.Fatalf("ActivityItem.properties.kind declares no enum in %s: an open string accepts any "+
			"spec name, which is exactly the drift this gate exists to catch", contractPath)
	}
	declared := make(map[string]bool, len(kind.Enum))
	for _, value := range kind.Enum {
		declared[value] = true
	}
	return declared
}

// The wire bound is the contract's, and the store enforces it. Both halves are
// read here so neither can move alone: a lowered maxLength with an unchanged
// constant publishes a promise the server breaks, and the reverse truncates
// silently.
func TestTheStoreCapsTheFreeTextColumnsAtTheLengthTheContractPublishes(t *testing.T) {
	for _, tc := range []struct {
		property string
		bound    int
	}{
		{"summary", summaryBound},
		{"degrade_reason", degradeReasonBound},
	} {
		published := activityItemProperty(t, tc.property).MaxLength
		if published == nil {
			t.Errorf("ActivityItem.properties.%s publishes no maxLength, so a client cannot size it "+
				"and nothing measures the server's own cap of %d against the contract", tc.property, tc.bound)
			continue
		}
		if *published != tc.bound {
			t.Errorf("ActivityItem.properties.%s publishes maxLength %d but the store caps at %d: "+
				"one of the two is lying to whoever reads it", tc.property, *published, tc.bound)
		}
	}
}

// contractPath is the authoritative document, read from disk on purpose: the
// generated Go type cannot disagree with itself, and it is the disagreement
// between the two trees that these gates exist to find.
const contractPath = "../../../api/crm.yaml"

// activityItemProperty walks to one ActivityItem property. Every step fails
// loudly: a moved or renamed schema must break these tests rather than let them
// silently compare against an empty set.
func activityItemProperty(t *testing.T, name string) contractProperty {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(contractPath))
	if err != nil {
		t.Fatalf("read the contract at %s: %v", contractPath, err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]contractProperty `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s as YAML: %v", contractPath, err)
	}

	item, ok := doc.Components.Schemas["ActivityItem"]
	if !ok {
		t.Fatalf("components.schemas.ActivityItem is absent from %s: the schema these tests guard "+
			"has moved or been renamed, so nothing is guarding it any more", contractPath)
	}
	property, ok := item.Properties[name]
	if !ok {
		t.Fatalf("components.schemas.ActivityItem.properties.%s is absent from %s: it is no longer "+
			"where this gate reads it", name, contractPath)
	}
	return property
}
