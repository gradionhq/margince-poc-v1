// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Every filter this module declares narrows something — the deal and project
// half of the check people/listfilters_test.go states: a binding that parses
// its operand and writes nowhere runs the list WIDER than the caller asked,
// and does it while looking exactly like a narrowed answer.
func TestEveryDeclaredDealsFilterNarrowsSomething(t *testing.T) {
	id := ids.NewV7().String()
	assertEveryFilterNarrows(t, "deal", dealListFilters, map[string]string{
		"organization_id": id, "owner_id": id, "partner_org_id": id, "partner_sourced": "true",
		"pipeline_id": id, "project_id": id, "stage_id": id, "stalled": "false", "status": "open",
	})
	assertEveryFilterNarrows(t, "project", projectListFilters, map[string]string{
		"key": "ACME", "organization_id": id, "owner_id": id, "phase": "delivering",
	})
}

// The vocabulary a caller is offered is the vocabulary the store binds.
func TestTheOfferedDealsVocabularyIsTheBoundVocabulary(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct {
		entity datasource.EntityType
		bound  []string
	}{
		{datasource.EntityDeal, dealListFilters.Names()},
		{datasource.EntityProject, projectListFilters.Names()},
	} {
		if got := p.ListFilters(tc.entity); !slices.Equal(got, tc.bound) {
			t.Errorf("%s offers %v, binds %v", tc.entity, got, tc.bound)
		}
	}
}

// An entity type this module does not enumerate offers no filters.
func TestAnEntityThisModuleDoesNotListOffersNoFilters(t *testing.T) {
	if got := (&Provider{}).ListFilters(datasource.EntityPerson); len(got) != 0 {
		t.Errorf("person is not this module's to list, yet it offers %v", got)
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
