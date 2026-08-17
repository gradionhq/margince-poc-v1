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

// Filter state that is not a tree at all is refused where it is written, by the
// same surface that accepts the rest of the view.
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

// contractViewResources is the SavedViewResource enum as the contract declares
// it. Hand-listed, which is a snapshot rather than a derivation: an eighth
// member added to crm.yaml and forgotten here leaves this test green. The
// derived form belongs in the backend-root suite, where contractSchema already
// parses api/crm.yaml — tracked separately rather than half-built here.
var contractViewResources = []crmcontracts.SavedViewResource{
	crmcontracts.SavedViewResourceActivities,
	crmcontracts.SavedViewResourceDeals,
	crmcontracts.SavedViewResourceLeads,
	crmcontracts.SavedViewResourceOrganizations,
	crmcontracts.SavedViewResourcePartners,
	crmcontracts.SavedViewResourcePeople,
	crmcontracts.SavedViewResourceProjects,
}

// Every contract member is either filterable or declared deliberately not, in
// the direction that can actually fail.
//
// The hazard is a resource MISSING from viewResourceToEngine: validateViewFilter
// passes it through unchecked while SavedViewFilterSource refuses it at export
// — the accepted-at-write, refused-at-read split this gate exists to close.
// Iterating the map alone only proves its existing entries are live, which is
// the one direction that cannot reintroduce the bug.
//
// WHAT THIS DOES NOT SAY is whether the database will store a view over that
// resource. Filterability and storability are separate vocabularies with
// separate authorities, and they disagree today — the saved_view.resource CHECK
// is the authority, and the integration lane compares against it.
func TestEveryViewResourceIsFilterableOrDeclaredOtherwise(t *testing.T) {
	for _, resource := range contractViewResources {
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

// The other direction, which the enum-first loop above gives up: a key in
// viewResourceToEngine that no contract member names is a mapping nothing can
// ever reach, and it would sit there reading as coverage.
func TestEveryMappedViewResourceIsAContractMember(t *testing.T) {
	for name := range viewResourceToEngine {
		if !crmcontracts.SavedViewResource(name).Valid() {
			t.Errorf("viewResourceToEngine maps %q, which the contract's resource enum does not declare", name)
		}
	}
}

// A tree that marshals but does not decode into a predicate — `field` is a
// string in the canonical shape, so a number there is well-formed JSON and an
// invalid filter. The decode answers a sentinel carrying NO wire field, which
// is what lets each surface name its own.
func TestAnUndecodableTreeAnswersASentinelWithNoWireField(t *testing.T) {
	_, err := predicateFromDefinition(map[string]any{"field": 5, "op": "eq", "value": "x"})

	if !errors.Is(err, errNotAFilterTree) {
		t.Fatalf("err = %v, want errNotAFilterTree", err)
	}
	var bad *BadInputError
	if errors.As(err, &bad) {
		t.Errorf("the decoder named a wire field (%q) it cannot know", bad.Field)
	}
}

// An undecodable tree names a DIFFERENT field depending on which surface
// carried it — driven through the real entry points, because the claim is about
// what each surface answers, and a direct call to the shared helper would only
// prove it returns the string it was handed. Neither entry point reaches the
// pool for this input, so both belong in the unit lane.
func TestAnUndecodableTreeIsNamedForTheSurfaceThatCarriedIt(t *testing.T) {
	store := (&Store{}).WithFieldCatalog(stubFilterable{})
	tree := map[string]any{"field": 5, "op": "eq", "value": "x"}

	for _, c := range []struct {
		surface string
		refuse  func() error
		field   string
	}{
		{
			surface: "a dynamic list, which sends the tree as `definition`",
			refuse:  func() error { return store.validateSegmentDefinition(context.Background(), "person", tree) },
			field:   definitionField,
		},
		{
			surface: "a saved view, which sends it inside `query`",
			refuse: func() error {
				return store.validateViewFilter(context.Background(), "people", map[string]any{"filter": tree})
			},
			field: viewQueryField,
		},
	} {
		t.Run(c.surface, func(t *testing.T) {
			err := c.refuse()

			var bad *BadInputError
			if !errors.As(err, &bad) {
				t.Fatalf("err = %v, want a BadInputError", err)
			}
			if bad.Field != c.field {
				t.Errorf("field = %q, want %q — the caller is told to fix a key they never sent", bad.Field, c.field)
			}
		})
	}
}

// A failure that is not the tree's shape is not the caller's to fix, so it must
// not arrive dressed as their field error: a catalogue that cannot answer would
// otherwise read as an invalid filter.
func TestACatalogueFailureIsNotDressedAsAFieldFault(t *testing.T) {
	boom := errors.New("catalog unreachable")
	store := (&Store{}).WithFieldCatalog(stubFilterable{err: boom})

	err := store.validateViewFilter(context.Background(), "people", map[string]any{
		"filter": map[string]any{"field": "owner_id", "op": "eq", "value": "x"},
	})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the catalogue's own error", err)
	}
	var bad *BadInputError
	if errors.As(err, &bad) {
		t.Errorf("a failed catalogue read was reported as a bad %q", bad.Field)
	}
}
