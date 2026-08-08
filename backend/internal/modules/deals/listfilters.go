// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// What an enumeration of this module's records may be narrowed by — the deal
// and project halves of the same rule people/listfilters.go states: the names
// are the contract's list-operation parameters, and this file says which of
// them this store answers and what each one narrows.

import (
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The filter names this module answers, spelled once each. They are wire names
// — a caller's query parameter — which is why they are not the column
// constants they happen to match today.
const (
	filterOrganizationID = "organization_id"
	filterOwnerID        = "owner_id"
	filterPartnerOrgID   = "partner_org_id"
	filterPartnerSourced = "partner_sourced"
	filterPipelineID     = "pipeline_id"
	filterProjectID      = "project_id"
	filterStageID        = "stage_id"
	filterStalled        = "stalled"
	filterStatus         = "status"
	filterKey            = "key"
	filterPhase          = "phase"
)

var dealListFilters = storekit.FilterSet[ListDealsInput]{
	filterOrganizationID: storekit.FilterID(
		func(in *ListDealsInput, id *ids.OrganizationID) { in.OrganizationID = id }),
	filterOwnerID: storekit.FilterID(func(in *ListDealsInput, id *ids.UserID) { in.OwnerID = id }),
	filterPartnerOrgID: storekit.FilterID(
		func(in *ListDealsInput, id *ids.OrganizationID) { in.PartnerOrgID = id }),
	filterPartnerSourced: storekit.FilterFlag(func(in *ListDealsInput, v *bool) { in.PartnerSourced = v }),
	filterPipelineID:     storekit.FilterID(func(in *ListDealsInput, id *ids.PipelineID) { in.PipelineID = id }),
	filterProjectID:      storekit.FilterID(func(in *ListDealsInput, id *ids.ProjectID) { in.ProjectID = id }),
	filterStageID:        storekit.FilterID(func(in *ListDealsInput, id *ids.StageID) { in.StageID = id }),
	filterStalled:        storekit.FilterFlag(func(in *ListDealsInput, v *bool) { in.Stalled = v }),
	filterStatus:         storekit.FilterWord(func(in *ListDealsInput, v *string) { in.Status = v }),
}

var projectListFilters = storekit.FilterSet[ListProjectsInput]{
	filterKey: storekit.FilterWord(func(in *ListProjectsInput, v *string) { in.Key = v }),
	filterOrganizationID: storekit.FilterID(
		func(in *ListProjectsInput, id *ids.OrganizationID) { in.OrganizationID = id }),
	filterOwnerID: storekit.FilterID(func(in *ListProjectsInput, id *ids.UserID) { in.OwnerID = id }),
	filterPhase:   storekit.FilterWord(func(in *ListProjectsInput, v *string) { in.Phase = v }),
}

// ListFilters names what SearchEntity can narrow one entity type by.
func (p *Provider) ListFilters(t datasource.EntityType) []string {
	switch t {
	case datasource.EntityDeal:
		return dealListFilters.Names()
	case datasource.EntityProject:
		return projectListFilters.Names()
	default:
		return nil
	}
}
