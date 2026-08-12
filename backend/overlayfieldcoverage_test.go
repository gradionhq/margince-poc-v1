// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// An overlay-mode installation serves its reads from a mirror of the
// customer's incumbent CRM, and every contract field that mirror cannot fill
// is a blank a user sees. The blank is not the defect — an undecided blank
// is. This gate asks the contract what fields exist and demands the overlay
// registry have an answer for each, so a field added to the core model
// cannot pass unnoticed into a surface that silently drops it.
//
// Scope is the ARMED entities only. deal, lead and activity carry mappings
// whose targets predate the registry; arming them is its own work, and this
// gate names them so the remainder stays visible in code.

import (
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// contractSchemaNameFor maps a canonical overlay entity to the contract
// schema publishing its fields. Both spellings are load-bearing and differ
// (person → Person), so the correspondence is declared rather than derived
// from a capitalization rule that would break on the first two-word type.
// gatekit:fixture the contract schema name each overlay entity's fields are
// read from — expected wiring, not a waived cost.
var contractSchemaNameFor = map[string]string{
	"person":       "Person",
	"organization": "Organization",
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
		t.Fatalf("api/crm.yaml declares no properties on %s; the field list this gate derives from has moved", schemaName)
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
