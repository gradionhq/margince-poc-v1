// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The ai_task.state_changed payload's closed vocabularies must equal the
// ai_task_run column CHECKs they are projected into.
//
// This binding has no Go half to lean on, and that is deliberate: crmcontracts
// is one package shared with the whole generated API surface, where
// oapi-codegen would name an inline enum's constants after its VALUES —
// 'Failed', 'Queued', 'Records' — and 'Failed' is already taken there. So the
// generated struct carries plain strings, and the contract's enum is held to
// the schema HERE instead. Both sides are derived: the enum from the YAML, the
// set from the migration's CHECK. A value added to one and not the other is a
// payload the projection accepts and the INSERT rejects, which surfaces as a
// wedged consumer group rather than as a validation error.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
)

// The wire's state vocabulary is the projection's PLUS one value nothing
// stores.
//
// `stalled` is derived at read time from a stored lease, so it can never appear
// in the column — and the reason it must appear on the contract anyway is the
// whole no-stale-state requirement: a live occurrence past its lease is
// reported stalled, and a client whose enum does not carry the word renders
// nothing for the case the feature exists to show.
//
// Held in BOTH directions on purpose. A value added to the CHECK and not to the
// contract is a state the server can emit and the client cannot name; one added
// to the contract and not to the CHECK is a promise no writer can keep.
func TestTheWireStateEnumIsTheProjectionsPlusTheDerivedOne(t *testing.T) {
	stored := aiTaskRunCheckValues(t, "state")
	wire := crmYAMLEnum(t, "AiActivityItem", "state")

	want := append(slices.Clone(stored), aiactivity.StateStalled)
	slices.Sort(want)
	if !slices.Equal(wire, want) {
		t.Errorf("the contract's state enum is %v but ai_task_run admits %v plus the derived %q; "+
			"a value on one side only is either a state the client cannot name or a promise no writer can keep",
			wire, stored, aiactivity.StateStalled)
	}
}

// crmYAMLEnum reads one property's enum out of the authoritative contract.
func crmYAMLEnum(t *testing.T, schema, property string) []string {
	t.Helper()
	prop, ok := crmYAMLSchemas(t)[schema].Properties[property]
	if !ok || len(prop.Enum) == 0 {
		t.Fatalf("%s.%s declares no enum in api/crm.yaml", schema, property)
	}
	out := slices.Clone(prop.Enum)
	slices.Sort(out)
	return out
}

// crmYAMLNamedEnum reads a schema that IS an enum rather than one that has a
// property carrying one. A vocabulary two places must agree on gets its own
// schema, so reading it needs its own accessor — the property walk above simply
// finds nothing there and reports it as an absent enum.
func crmYAMLNamedEnum(t *testing.T, schema string) []string {
	t.Helper()
	declared := crmYAMLSchemas(t)[schema]
	if len(declared.Enum) == 0 {
		t.Fatalf("schema %s declares no enum in api/crm.yaml", schema)
	}
	out := slices.Clone(declared.Enum)
	slices.Sort(out)
	return out
}

// crmYAMLSchema is as much of one contract schema as these gates read: its own
// enum, when it is one, and its properties' enums when it is an object.
type crmYAMLSchema struct {
	Enum       []string `yaml:"enum"`
	Properties map[string]struct {
		Enum []string `yaml:"enum"`
	} `yaml:"properties"`
}

func crmYAMLSchemas(t *testing.T) map[string]crmYAMLSchema {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]crmYAMLSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	raw, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	return doc.Components.Schemas
}

func TestTheAiTaskPayloadEnumsMatchTheProjectionsChecks(t *testing.T) {
	for _, b := range []struct{ property, column string }{
		{"state", "state"},
		{"quantity_unit", "quantity_unit"},
	} {
		want := aiTaskRunCheckValues(t, b.column)
		got := aiTaskPayloadEnum(t, b.property)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the payload enum is %v but ai_task_run.%s admits %v — a value on one side only is an event the projection accepts and the INSERT rejects",
				b.property, got, b.column, want)
		}
	}
}

// aiTaskPayloadEnum reads one property's enum out of the internal payload
// contract, sorted so the comparison is about membership and not authoring
// order.
func aiTaskPayloadEnum(t *testing.T, property string) []string {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	raw, err := os.ReadFile("api/internal-events.yaml")
	if err != nil {
		t.Fatalf("reading the internal payload contract: %v", err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the internal payload contract: %v", err)
	}
	schema, ok := doc.Components.Schemas["InternalEventAiTaskStateChanged"]
	if !ok {
		t.Fatal("api/internal-events.yaml declares no InternalEventAiTaskStateChanged")
	}
	prop, ok := schema.Properties[property]
	if !ok || len(prop.Enum) == 0 {
		t.Fatalf("InternalEventAiTaskStateChanged.%s declares no enum", property)
	}
	out := slices.Clone(prop.Enum)
	slices.Sort(out)
	return out
}

// aiTaskRunCheckValues extracts one column's `IN (...)` membership set from the
// ai_task_run migration, found by its constraint rather than by a filename an
// author renumbers on the way to a merge. The search is confined to the CREATE
// TABLE body so the rank function's own `state text` parameter cannot answer
// for the column.
func aiTaskRunCheckValues(t *testing.T, column string) []string {
	t.Helper()
	matches, err := filepath.Glob("migrations/core/*_ai_task_run.up.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one ai_task_run migration, found %v (err %v)", matches, err)
	}
	raw, err := os.ReadFile(matches[0]) // #nosec G304 -- the path comes from this test's own glob
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	body := string(raw)
	start := strings.Index(body, "CREATE TABLE ai_task_run")
	end := strings.Index(body, "CREATE INDEX")
	if start < 0 || end <= start {
		t.Fatalf("%s does not hold a CREATE TABLE ai_task_run body followed by its indexes", matches[0])
	}
	body = body[start:end]

	// Anchored at the column, then the first IN list that follows it — which is
	// its own CHECK, since a column definition ends at the next one.
	re := regexp.MustCompile(`(?s)\b` + regexp.QuoteMeta(column) + `\s+text\b.*?IN \(([^)]*)\)`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CHECK naming ai_task_run.%s in %s", column, matches[0])
	}
	var out []string
	for _, v := range strings.Split(m[1], ",") {
		out = append(out, strings.Trim(strings.TrimSpace(v), "'"))
	}
	slices.Sort(out)
	return out
}
