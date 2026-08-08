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

// Each entity type is offered ITS OWN vocabulary.
//
// The mapping is the part that can be wrong without anything else noticing: a
// switch arm pointing at a sibling's table hands out a vocabulary the store
// then refuses, and comparing what ListFilters returns against the same table
// it returns would never see it. So the expectations are written out — a
// person is listed by owner and nothing else, a lead by owner and its status.
func TestEachEntityIsOfferedItsOwnVocabulary(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct {
		entity datasource.EntityType
		want   []string
	}{
		{datasource.EntityPerson, []string{"owner_id"}},
		{datasource.EntityOrganization, []string{"lifecycle", "owner_id", "relationship_type"}},
		{datasource.EntityLead, []string{"owner_id", "status"}},
	} {
		if got := p.ListFilters(tc.entity); !slices.Equal(got, tc.want) {
			t.Errorf("%s is offered %v, want %v", tc.entity, got, tc.want)
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
