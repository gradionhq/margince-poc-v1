// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Every filter this module declares narrows something.
//
// A binding that parses its operand and writes nowhere is the one defect this
// table's shape cannot rule out by construction, and it is invisible at the
// call site: the list runs, rows come back, and they are the rows of a WIDER
// question than the caller asked. The check is per-filter and derived from the
// table itself, so a filter added tomorrow is covered without anyone
// remembering to cover it.
func TestEveryDeclaredPeopleFilterNarrowsSomething(t *testing.T) {
	owner := ids.NewV7().String()
	assertEveryFilterNarrows(t, "person", personListFilters, map[string]string{"owner_id": owner})
	assertEveryFilterNarrows(t, "organization", organizationListFilters, map[string]string{
		"lifecycle": "customer", "owner_id": owner, "relationship_type": "partner",
	})
	assertEveryFilterNarrows(t, "lead", leadListFilters, map[string]string{
		"owner_id": owner, "status": "working",
	})
}

// The vocabulary a caller is offered is the vocabulary the store binds. Two
// lists would drift, and the direction that drifts silently is the dangerous
// one: a name offered and not bound runs the list unnarrowed.
func TestTheOfferedVocabularyIsTheBoundVocabulary(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct {
		entity datasource.EntityType
		bound  []string
	}{
		{datasource.EntityPerson, personListFilters.Names()},
		{datasource.EntityOrganization, organizationListFilters.Names()},
		{datasource.EntityLead, leadListFilters.Names()},
	} {
		if got := p.ListFilters(tc.entity); !slices.Equal(got, tc.bound) {
			t.Errorf("%s offers %v, binds %v", tc.entity, got, tc.bound)
		}
	}
}

// An entity type this module does not enumerate offers no filters rather than
// pretending to one type's vocabulary.
func TestAnEntityThisModuleDoesNotListOffersNoFilters(t *testing.T) {
	if got := (&Provider{}).ListFilters(datasource.EntityDeal); len(got) != 0 {
		t.Errorf("deal is not this module's to list, yet it offers %v", got)
	}
}

// assertEveryFilterNarrows applies each of a set's filters on its own and
// requires the input to have moved off its zero value.
func assertEveryFilterNarrows[I any](t *testing.T, entity string, set storekit.FilterSet[I], operands map[string]string) {
	t.Helper()
	for _, name := range set.Names() {
		operand, ok := operands[name]
		if !ok {
			t.Fatalf("%s declares the %q filter and this test has no operand for it, "+
				"so nothing here proves it narrows anything", entity, name)
		}
		var in, untouched I
		if err := set.Apply(&in, map[string]string{name: operand}); err != nil {
			t.Fatalf("%s: applying %s=%s: %v", entity, name, operand, err)
		}
		if reflect.DeepEqual(in, untouched) {
			t.Errorf("%s: the %q filter applied cleanly and narrowed nothing — "+
				"the list runs wider than the caller asked", entity, name)
		}
	}
}
