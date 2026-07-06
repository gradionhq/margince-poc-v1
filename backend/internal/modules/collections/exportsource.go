// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A saved view and a dynamic list are the two stored filter sources
// filtered export (B-E15.13) reuses instead of a bespoke filter: a saved
// view's filter state and a dynamic list's definition are already the
// canonical predicate representation (features/10 §3, one engine). The
// resolvers below hand compose the (resource, predicate) pair to run
// through SegmentEngine — behind the very same owner-only / row-scope
// gates the read endpoints apply, so an export can never widen what its
// source could show.

// FilterSource is a resolved export filter: the closed resource key the
// predicate engine is looked up by, plus the canonical predicate tree.
type FilterSource struct {
	Resource  string
	Predicate storekit.Predicate
}

// viewResourceToEngine maps a saved view's resource (the contract's plural
// spelling) to the predicate engine's table key. Resources with no segment
// engine (activities, partners — not predicate-leaf resources) are absent,
// so a view over them cannot be filter-exported.
var viewResourceToEngine = map[string]string{
	"people":        "person",
	"organizations": "organization",
	"deals":         "deal",
	"leads":         "lead",
}

// SavedViewFilterSource resolves a saved view to its export filter. It
// reads the view through GetSavedView, so the caller only ever exports
// their own view (owner-only; another user's or a missing view reads as
// absent). The predicate is the view query's `filter` state — the same
// canonical tree a dynamic list stores — decoded through the one parser.
func (s *Store) SavedViewFilterSource(ctx context.Context, id ids.UUID) (FilterSource, error) {
	view, err := s.GetSavedView(ctx, id)
	if err != nil {
		return FilterSource{}, err
	}
	resource, ok := viewResourceToEngine[view.Resource]
	if !ok {
		return FilterSource{}, &BadInputError{
			Field:  "view_id",
			Reason: fmt.Sprintf("a %s view has no filterable export engine", view.Resource),
		}
	}
	rawFilter, ok := view.Query["filter"]
	if !ok {
		return FilterSource{}, &BadInputError{
			Field:  "view_id",
			Reason: "this saved view carries no filter state to export",
		}
	}
	filterMap, ok := rawFilter.(map[string]any)
	if !ok {
		return FilterSource{}, &BadInputError{
			Field:  "view_id",
			Reason: "this saved view's filter state is not a filter tree",
		}
	}
	pred, err := predicateFromDefinition(filterMap)
	if err != nil {
		return FilterSource{}, err
	}
	return FilterSource{Resource: resource, Predicate: pred}, nil
}

// ListFilterSource resolves a dynamic list to its export filter. It reads
// the list through GetList, so the export is bounded by the list's own
// row-scope gate. A static list has explicit members rather than a filter,
// so it is rejected here (its rows are exported through its members
// endpoint, not the predicate engine).
func (s *Store) ListFilterSource(ctx context.Context, id ids.UUID) (FilterSource, error) {
	list, err := s.GetList(ctx, id)
	if err != nil {
		return FilterSource{}, err
	}
	if list.ListType != "dynamic" {
		return FilterSource{}, &BadInputError{
			Field:  "list_id",
			Reason: "a static list carries explicit members, not a filter; export it through its members",
		}
	}
	pred, err := predicateFromDefinition(list.Definition)
	if err != nil {
		return FilterSource{}, err
	}
	return FilterSource{Resource: list.EntityType, Predicate: pred}, nil
}
