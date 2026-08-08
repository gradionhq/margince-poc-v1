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
// A contract parameter absent below is absent on purpose: `tag` is declared by
// listPeople and no column here holds tags, so listing by it would return
// everything while looking exactly like a narrowed answer.

import (
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

var personListFilters = storekit.FilterSet[ListPeopleInput]{
	"owner_id": storekit.FilterID("owner_id", func(in *ListPeopleInput, id *ids.UserID) { in.OwnerID = id }),
}

var organizationListFilters = storekit.FilterSet[ListOrganizationsInput]{
	"lifecycle": storekit.FilterWord(func(in *ListOrganizationsInput, v *string) { in.Lifecycle = v }),
	"owner_id": storekit.FilterID("owner_id",
		func(in *ListOrganizationsInput, id *ids.UserID) { in.OwnerID = id }),
	"relationship_type": storekit.FilterWord(func(in *ListOrganizationsInput, v *string) { in.RelationshipType = v }),
}

var leadListFilters = storekit.FilterSet[ListLeadsInput]{
	"owner_id": storekit.FilterID("owner_id", func(in *ListLeadsInput, id *ids.UserID) { in.OwnerID = id }),
	"status":   storekit.FilterWord(func(in *ListLeadsInput, v *string) { in.Status = v }),
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
