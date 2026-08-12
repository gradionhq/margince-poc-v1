// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// An overlay-mode installation serves its reads from a mirror of the
// customer's incumbent CRM, and every contract field that mirror cannot fill
// is a blank a user sees. The blank is not the defect — an undecided blank
// is. This gate asks the contract what fields exist and demands the overlay
// registry have an answer for each, so a field added to the core model
// cannot pass unnoticed into a surface that silently drops it.
//
// Scope is the ARMED entities only. The registry declares the rest as
// unarmed entities carrying their own reason, and this run names them, so
// what is still undecided stays visible in code rather than in a backlog
// note.

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// contractFile is the contract this gate derives its field lists from,
// relative to this package's own directory.
const contractFile = "../../api/crm.yaml"

// contractSchemaNameFor maps a canonical overlay entity to the contract
// schema publishing its fields. Both spellings are load-bearing and differ
// (person → Person), so the correspondence is declared rather than derived
// from a capitalization rule that would break on the first two-word type.
// gatekit:fixture the contract schema name each overlay entity's fields are
// read from — expected wiring, not a waived cost.
var contractSchemaNameFor = map[string]string{
	"person": "Person",
}

func TestArmedOverlayEntitiesDispositionEveryContractField(t *testing.T) {
	var armed, unarmed []string
	for _, entity := range overlay.FieldBindings() {
		if !entity.Armed {
			unarmed = append(unarmed, entity.Entity)
			continue
		}
		armed = append(armed, entity.Entity)
		checkEntityCoverage(t, entity)
	}
	if len(armed) == 0 {
		t.Fatal("no overlay entity is armed; this gate is watching nothing")
	}
	sort.Strings(unarmed)
	t.Logf("armed: %s; not yet armed: %s", strings.Join(armed, ", "), strings.Join(unarmed, ", "))
}

// checkEntityCoverage compares one armed entity's bindings against the
// contract fields it must account for, in both directions: a contract field
// with no binding is an undecided blank, and a binding for a field the
// contract no longer publishes is a stale entry that would outlive its slot.
func checkEntityCoverage(t *testing.T, entity overlay.EntityBinding) {
	t.Helper()
	schemaName, ok := contractSchemaNameFor[entity.Entity]
	if !ok {
		t.Errorf("overlay entity %q is armed but names no contract schema in contractSchemaNameFor", entity.Entity)
		return
	}
	properties := contractSchema(t, schemaName).Properties
	if len(properties) == 0 {
		t.Errorf("api/crm.yaml declares no properties on %s; the field list this gate derives from has moved", schemaName)
		return
	}

	bound := make(map[string]bool, len(entity.Bindings))
	for _, b := range entity.Bindings {
		bound[b.WireSlot] = true
		if _, published := properties[b.WireSlot]; !published {
			t.Errorf("overlay %s binds %q, but api/crm.yaml's %s no longer publishes that field. "+
				"Delete the binding in internal/modules/overlay/fieldbinding.go.",
				entity.Entity, b.WireSlot, schemaName)
		}
	}

	var undecided []string
	for name := range properties {
		if !bound[name] {
			undecided = append(undecided, name)
		}
	}
	sort.Strings(undecided)
	for _, name := range undecided {
		t.Errorf("api/crm.yaml's %s publishes %q, but the overlay registry does not say whether the mirror carries it. "+
			"Add a FieldBinding for %q to %sBindings in internal/modules/overlay/fieldbinding.go — "+
			"mapped (name the incumbent property), deferred (with an issue URL), unmappable or native_only (with a reason).",
			schemaName, name, name, entity.Entity)
	}
}

// contractSchemaFields is the slice of one components.schemas entry this gate
// reads. Typing Properties as a mapping is what keeps a malformed contract a
// parse failure instead of a silently empty field list: yaml.v3 skips keys the
// struct does not name, but it errors when the node it is asked to decode as a
// mapping is anything else.
//
// This reads the properties written inline under the named schema, so a schema
// recomposed through $ref or allOf would yield a partial list. Every schema
// this gate names is inline, and neither degradation is silent — an empty list
// fails the coverage check outright, and a partial one fails as bindings the
// contract no longer appears to publish.
type contractSchemaFields struct {
	Properties map[string]yaml.Node `yaml:"properties"`
}

func contractSchema(t *testing.T, name string) contractSchemaFields {
	t.Helper()
	raw, err := os.ReadFile(contractFile)
	if err != nil {
		t.Fatalf("reading %s: %v", contractFile, err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]contractSchemaFields `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", contractFile, err)
	}
	schema, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("%s declares no components.schemas.%s", contractFile, name)
	}
	return schema
}
