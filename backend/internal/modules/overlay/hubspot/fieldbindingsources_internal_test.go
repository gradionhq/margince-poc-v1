// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// A mapped binding makes two claims: that a canonical key holds the value, and
// that named incumbent properties are where it comes from. The wire gate in
// compose proves the first end to end, but it can only prove the second against
// a hand-written fixture — so a binding naming a property the mapping never
// reads goes green the moment somebody adds that property to the fixture.
// propertyNames is the production answer to what a class consumes: it builds
// the `properties=` list the adapter asks HubSpot for, so a property missing
// from it is a property that never arrives, whatever any fixture supplies.
func TestEveryMappedBindingNamesAPropertyItsMappingReads(t *testing.T) {
	checked := 0
	for _, entity := range overlay.FieldBindings() {
		// Only an armed entity has been decided field by field; deal, lead and
		// activity carry mappings chosen before this registry existed, and hold
		// no bindings to check.
		if !entity.Armed {
			continue
		}
		classes, consumed := propertiesReadFor(t, entity.Entity)
		for _, b := range entity.Bindings {
			if b.Disposition != overlay.DispositionMapped {
				continue
			}
			checked++
			checkIncumbentProperties(t, entity.Entity, b, classes, consumed)
		}
	}
	if checked == 0 {
		t.Fatal("no armed entity declares a mapped binding; this gate would pass while checking nothing")
	}
}

// checkIncumbentProperties holds one mapped binding's source properties to what
// its entity's mappings actually request.
func checkIncumbentProperties(t *testing.T, entity string, b overlay.FieldBinding, classes []string, consumed map[string]bool) {
	t.Helper()
	source := strings.Join(classes, "/")
	for _, property := range b.Incumbent {
		if consumed[property] {
			continue
		}
		t.Errorf("overlay %s.%s is declared mapped from %q, but the %s mapping never asks HubSpot for that "+
			"property, so no value under that name ever reaches the mirror. Either give the %s mapping in "+
			"mapping_hs.go a field reading %q, or correct Incumbent on that binding in "+
			"internal/modules/overlay/fieldbinding.go to the property the mapping really reads.",
			entity, b.WireSlot, property, source, source, property)
	}
}

// propertiesReadFor answers the incumbent classes backing one canonical entity
// and the union of the properties their mappings request. The union is what a
// binding is held to because a canonical entity may be backed by several
// classes, each of which spells its own properties.
func propertiesReadFor(t *testing.T, entity string) ([]string, map[string]bool) {
	t.Helper()
	classes, ok := IncumbentClassesFor(entity)
	if !ok {
		t.Fatalf("IncumbentClassesFor(%q) found no incumbent class, but the overlay registry arms that entity; "+
			"one of the two names an entity the other does not know", entity)
	}
	consumed := map[string]bool{}
	for _, class := range classes {
		m, found := Mapping(class)
		if !found {
			t.Fatalf("Mapping(%q): want the mapping IncumbentClassesFor(%q) just named", class, entity)
		}
		for _, property := range propertyNames(m) {
			consumed[property] = true
		}
	}
	return classes, consumed
}
