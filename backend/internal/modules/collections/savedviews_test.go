// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// A saved view's filter is checked against the same vocabulary a dynamic list's
// definition is, and at the same moment — when it is written. These cover the
// decision the gate makes; that the refusal reaches the wire as a 422 is proven
// over the real store in the integration suite.

import (
	"context"
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

func TestASavedViewFilterNamingAnUnknownFieldIsRefused(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{})

	err := store.validateViewFilter(context.Background(), "people", map[string]any{
		"filter": map[string]any{"field": "favourite_colour", "op": "eq", "value": "blue"},
	})

	var perr *storekit.PredicateError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a PredicateError", err)
	}
	if perr.Code != storekit.CodeFilterFieldNotAllowed {
		t.Errorf("code = %q, want %q", perr.Code, storekit.CodeFilterFieldNotAllowed)
	}
}

// The vocabulary a view is checked against is this workspace's, custom columns
// included — the same merge membership evaluation and export resolve. A gate
// reading only the core fields would refuse a legitimate cf_ filter, which is a
// worse failure than the one it was added to prevent.
func TestASavedViewFilterOnACustomColumnIsAccepted(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{cols: map[string][]fieldcatalog.Column{
		"person": {{Name: "cf_tier", Type: fieldcatalog.TypeText}},
	}})

	err := store.validateViewFilter(context.Background(), "people", map[string]any{
		"filter": map[string]any{"field": "cf_tier", "op": "eq", "value": "gold"},
	})
	if err != nil {
		t.Fatalf("a filter on an active custom column was refused: %v", err)
	}
}

// A view is columns, sort and grouping as much as it is a filter, so the three
// states with nothing to check must pass rather than fail closed.
func TestASavedViewWithNothingToValidateIsAccepted(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{})
	unknownField := map[string]any{"field": "favourite_colour", "op": "eq", "value": "blue"}

	for _, c := range []struct {
		name     string
		resource string
		query    map[string]any
	}{
		{"no filter state at all", "people", map[string]any{"columns": []any{"full_name"}}},
		{"a cleared filter", "people", map[string]any{"filter": nil}},
		{"a resource with no segment engine", "activities", map[string]any{"filter": unknownField}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := store.validateViewFilter(context.Background(), c.resource, c.query); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// Filter state that is not a tree at all is refused where it is written rather
// than where it is read. The export path has always answered this; the point of
// the gate is that both surfaces now answer it.
func TestASavedViewFilterThatIsNotATreeIsRefused(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{})

	err := store.validateViewFilter(context.Background(), "people", map[string]any{
		"filter": "owner_id eq me",
	})

	var bad *BadInputError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a BadInputError", err)
	}
	if bad.Field != viewQueryField {
		t.Errorf("field = %q, want %q", bad.Field, viewQueryField)
	}
}

// nonFilterableViewResources are the saved-view resources that deliberately
// have no segment engine: they are not predicate-leaf resources, so a view over
// one carries view state without a filter this module can check. Named here so
// the gate below can tell "intentionally unfilterable" from "forgotten".
var nonFilterableViewResources = map[string]bool{
	string(crmcontracts.SavedViewResourceActivities): true,
	string(crmcontracts.SavedViewResourcePartners):   true,
}

// Derived from the CONTRACT's enum, in the direction that can actually fail.
//
// The hazard is a resource MISSING from viewResourceToEngine: validateViewFilter
// passes it through unchecked while SavedViewFilterSource refuses it at export
// — the accepted-at-write, refused-at-read split this gate exists to close.
// Iterating the map instead only proves its existing entries are live, which is
// the one direction that cannot reintroduce the bug. So every contract member
// must be either filterable or listed as deliberately not.
func TestEveryViewResourceIsFilterableOrDeclaredOtherwise(t *testing.T) {
	for _, resource := range []crmcontracts.SavedViewResource{
		crmcontracts.SavedViewResourceActivities,
		crmcontracts.SavedViewResourceDeals,
		crmcontracts.SavedViewResourceLeads,
		crmcontracts.SavedViewResourceOrganizations,
		crmcontracts.SavedViewResourcePartners,
		crmcontracts.SavedViewResourcePeople,
		crmcontracts.SavedViewResourceProjects,
	} {
		name := string(resource)
		key, filterable := viewResourceToEngine[name]
		switch {
		case filterable && nonFilterableViewResources[name]:
			t.Errorf("view resource %q is both mapped to an engine and declared unfilterable", name)
		case filterable:
			if _, live := segmentEngines[key]; !live {
				t.Errorf("view resource %q maps to engine key %q, which has no segment engine", name, key)
			}
		case !nonFilterableViewResources[name]:
			t.Errorf("view resource %q has no engine and is not declared unfilterable, so a view over it accepts any filter at write and is refused at export", name)
		}
	}
}
