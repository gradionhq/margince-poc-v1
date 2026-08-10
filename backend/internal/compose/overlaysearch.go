// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay-mode SEARCH door, split from the get/list doors beside it in
// overlayread.go when that file reached the size ceiling.
//
// Search is its own concept rather than a sixth read: the get/list handlers each
// serve one entity type through one provider call, while this one walks every
// mirrored type, holds its own limit policy, and folds heterogeneous hits into a
// single ranked answer. It is the only read here that has to decide what a
// result MEANS across types, which is why the vocabulary and the clamp live with
// it rather than beside handlers that never consult them.

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// overlaySearchTypes is the entity-type order the overlay search walks. It is
// the MODULE's own list rather than a copy: the provider refuses a class the
// mirror cannot hold, and a second list here would let this door refuse one it
// can, or admit one it cannot, the moment a sixth is mirrored.
var overlaySearchTypes = overlay.MirroredEntityTypes()

// overlayMirroredTypes is the set of record types the mirror holds, keyed by
// the string form that is both datasource.EntityType and the generated
// agentPolicy.RecordType. Derived from overlaySearchTypes rather than
// re-listed, so reads and writes cannot drift.
var overlayMirroredTypes = func() map[string]bool {
	set := make(map[string]bool, len(overlaySearchTypes))
	for _, et := range overlaySearchTypes {
		set[string(et)] = true
	}
	return set
}()

// overlaySearchDefaultLimit sizes an overlay search page when the request
// names no limit — the shared Limit parameter's own default (crm.yaml's
// components.parameters.Limit: default 50), which /search refs like every
// other paged op. Overlay pages the same way native does or the two modes
// answer different pages for one query.
const overlaySearchDefaultLimit = 50

// overlaySearchMaxLimit is that same shared parameter's ceiling (maximum
// 200). A bound integer that slips past request validation (a negative or
// oversized ?limit=) must never reach a slice capacity, so the value is
// clamped here before it sizes any allocation.
const overlaySearchMaxLimit = 200

// clampOverlaySearchLimit maps a caller-supplied limit onto the shared
// parameter's 1..200 range so it is safe to use as an allocation size.
func clampOverlaySearchLimit(v int) int {
	switch {
	case v < 1:
		return 1
	case v > overlaySearchMaxLimit:
		return overlaySearchMaxLimit
	default:
		return v
	}
}

// Search shadows the global search: in overlay mode it is a
// visibility-filtered walk across entity types (design.md §4.5), served by
// the provider's own sweep so the MCP tool and this route answer one
// implementation rather than two.
//
// It PAGES. The walk has no ranking to interleave types by, so the sweep's
// cursor names where it stopped — the type plus that type's own mirror
// cursor — and `has_more` is true exactly when there is such a position to
// hand back.
func (s Server) Search(w http.ResponseWriter, r *http.Request, params crmcontracts.SearchParams) {
	ov, ok := s.overlayReadMode(w, r)
	if !ok {
		return
	}
	if !ov {
		s.searchHandlers.Search(w, r, params)
		return
	}
	types, ok := s.overlaySearchScope(w, r, params.Types)
	if !ok {
		return
	}
	query := datasource.SearchQuery{Text: params.Q, EntityTypes: types}
	if params.Cursor != nil {
		query.Cursor = *params.Cursor
	}
	if params.Limit != nil {
		query.Limit = clampOverlaySearchLimit(*params.Limit)
	} else {
		query.Limit = overlaySearchDefaultLimit
	}
	// An empty scope is an answerable question with an empty answer: the one
	// type the caller named is one they may not read. Serving it here rather
	// than through the provider is what keeps it a page instead of the 403 the
	// seam answers a tool with (overlaySearchScope's own rationale).
	res := datasource.SearchResult{}
	if len(types) > 0 {
		// An unmapped caller's existence-hiding ErrNotFound answers an EMPTY
		// page here, the same reading every list shadow gives it: a collection
		// read row-scopes down to nothing rather than 404ing, and both modes
		// must answer one contract the same way.
		var err error
		if res, err = s.sorDispatch.Search(r.Context(), query); err != nil && !errors.Is(err, apperrors.ErrNotFound) {
			httperr.Write(w, r, err)
			return
		}
	}
	hits, err := overlaySearchHits(res)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	page := crmcontracts.PageInfo{HasMore: res.HasMore}
	if res.NextCursor != "" {
		page.NextCursor = &res.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SearchResponse{Data: hits, Page: page})
}

// overlaySearchScope resolves which entity types this request sweeps.
//
// A named type the mirror does not hold is REFUSED rather than walked past.
// The mirror carries five object classes and the contract's `types` enum
// carries six, so a caller asking for projects would otherwise be handed an
// empty page reading "this workspace has no projects" — an answer about the
// records, when the truth is about the mode.
//
// It resolves ONE denial itself and leaves the rest to the provider, because
// the two doors answer a denial differently and each rule belongs where it is
// kept. Search shows a seat the object classes it can read and says nothing
// about the rest, so a caller who named a single type they may not read gets
// an empty page here rather than the 403 the seam gives a tool. Everything
// wider goes through unfiltered: the provider omits what the seat cannot see,
// and narrowing the list here as well would make the sweep's own posture
// depend on a filter applied a layer above it.
func (s Server) overlaySearchScope(
	w http.ResponseWriter, r *http.Request, named *[]crmcontracts.SearchParamsTypes,
) ([]datasource.EntityType, bool) {
	asked := overlaySearchTypes
	if named != nil {
		asked = make([]datasource.EntityType, 0, len(*named))
		for _, t := range *named {
			et := datasource.EntityType(t)
			if !overlayMirroredTypes[string(et)] {
				unsupportedOverlayParam(w, r, "types")
				return nil, false
			}
			asked = append(asked, et)
		}
	}
	if len(asked) != 1 {
		return asked, true
	}
	err := auth.Require(r.Context(), string(asked[0]), principal.ActionRead)
	switch {
	case err == nil:
		return asked, true
	case errors.Is(err, apperrors.ErrPermissionDenied):
		// The empty scope, which Search answers as an empty page.
		return nil, true
	default:
		// Not a fact about the caller's grants — this server not working.
		// Reading it as "may not see it" would answer a broken request chain
		// with an empty page and a 200.
		httperr.Write(w, r, err)
		return nil, false
	}
}

// overlaySearchHits assembles one swept page onto the wire, titling each hit
// by the type it came from.
func overlaySearchHits(res datasource.SearchResult) ([]crmcontracts.SearchResult, error) {
	typed := ContractSearchResults(res)
	for i, rec := range res.Records {
		fields, err := overlayRecordFields(rec)
		if err != nil {
			return nil, err
		}
		if title := overlayWireTitle(rec.Ref.Type, fields); title != "" {
			typed[i].Title = &title
		}
	}
	return typed, nil
}
