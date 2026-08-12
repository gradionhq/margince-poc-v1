// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What an enumeration of this module's records may be narrowed by.
//
// The NAMES are not authored here. They are the contract's own list-operation
// parameters, which `backend/tools/gen-recordfields` reads off crm.yaml for the
// agent surface to publish; what this file adds is the half the contract cannot
// know — which of them this store can actually answer, and the field each one
// narrows. The composition root publishes the intersection, so a filter
// declared by the contract and answered by no store is offered by neither.
//
// A contract parameter absent below is absent on purpose, and absent is not
// the same as unanswerable. What this set decides is narrower than what a store
// can answer: which names a TOOL publishes. Every one of them is rendered into
// the tool listing each step of a run re-sends, so growing it is the
// catalog-budget decision, not something a store learning to bind one more
// filter settles on its own.
//
// `tag`, `domain` and `min_score` were held back on exactly that rule and are
// published now, as the one decision #828 asked for. The first two are link
// predicates over rows this module holds in another table rather than in a
// column of its own, which is why they read as store detail rather than as
// filters — but a caller cannot see the difference, and an agent asked which
// contacts are tagged `vip` was enumerating and discarding client-side to
// answer it. `min_score` was never a new binding at all: the lead store has
// answered it since before #826, and nothing noticed the seam offering less
// than the store could.

import (
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The filter names this module answers, spelled once each. They are wire names
// — a caller's query parameter — which is why they are not the column
// constants they happen to match today.
const (
	filterOwnerID          = "owner_id"
	filterLifecycle        = "lifecycle"
	filterRelationshipType = "relationship_type"
	filterStatus           = "status"
	filterTag              = "tag"
	filterDomain           = "domain"
	filterMinScore         = "min_score"
)

var personListFilters = storekit.FilterSet[ListPeopleInput]{
	filterOwnerID: storekit.FilterID(func(in *ListPeopleInput, id *ids.UserID) { in.OwnerID = id }),
	filterTag:     storekit.FilterWord(func(in *ListPeopleInput, v *string) { in.Tag = v }),
}

var organizationListFilters = storekit.FilterSet[ListOrganizationsInput]{
	filterDomain:    storekit.FilterWord(func(in *ListOrganizationsInput, v *string) { in.Domain = v }),
	filterLifecycle: storekit.FilterWord(func(in *ListOrganizationsInput, v *string) { in.Lifecycle = v }),
	filterOwnerID:   storekit.FilterID(func(in *ListOrganizationsInput, id *ids.UserID) { in.OwnerID = id }),
	filterRelationshipType: storekit.FilterWord(
		func(in *ListOrganizationsInput, v *string) { in.RelationshipType = v }),
}

var leadListFilters = storekit.FilterSet[ListLeadsInput]{
	filterMinScore: storekit.FilterNumber(func(in *ListLeadsInput, v *int) { in.MinScore = v }),
	filterOwnerID:  storekit.FilterID(func(in *ListLeadsInput, id *ids.UserID) { in.OwnerID = id }),
	filterStatus:   storekit.FilterWord(func(in *ListLeadsInput, v *string) { in.Status = v }),
}

// ListFilters names what SearchEntity can narrow one entity type by. An entity
// type this module lists by nothing answers an empty vocabulary rather than
// nil, so a caller reads "no filters here" instead of "no such record type".
func (p *Provider) ListFilters(t datasource.EntityType) []string {
	switch t {
	case datasource.EntityPerson:
		return personListFilters.Names()
	case datasource.EntityOrganization:
		return organizationListFilters.Names()
	case datasource.EntityLead:
		return leadListFilters.Names()
	default:
		return nil
	}
}
