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

var dealListFilters = storekit.FilterSet[ListDealsInput]{
	"organization_id": storekit.FilterID("organization_id",
		func(in *ListDealsInput, id *ids.OrganizationID) { in.OrganizationID = id }),
	"owner_id": storekit.FilterID("owner_id", func(in *ListDealsInput, id *ids.UserID) { in.OwnerID = id }),
	"partner_org_id": storekit.FilterID("partner_org_id",
		func(in *ListDealsInput, id *ids.OrganizationID) { in.PartnerOrgID = id }),
	"partner_sourced": storekit.FilterFlag("partner_sourced", func(in *ListDealsInput, v *bool) { in.PartnerSourced = v }),
	"pipeline_id": storekit.FilterID("pipeline_id",
		func(in *ListDealsInput, id *ids.PipelineID) { in.PipelineID = id }),
	"project_id": storekit.FilterID("project_id",
		func(in *ListDealsInput, id *ids.ProjectID) { in.ProjectID = id }),
	"stage_id": storekit.FilterID("stage_id", func(in *ListDealsInput, id *ids.StageID) { in.StageID = id }),
	"stalled":  storekit.FilterFlag("stalled", func(in *ListDealsInput, v *bool) { in.Stalled = v }),
	"status":   storekit.FilterWord(func(in *ListDealsInput, v *string) { in.Status = v }),
}

var projectListFilters = storekit.FilterSet[ListProjectsInput]{
	"key": storekit.FilterWord(func(in *ListProjectsInput, v *string) { in.Key = v }),
	"organization_id": storekit.FilterID("organization_id",
		func(in *ListProjectsInput, id *ids.OrganizationID) { in.OrganizationID = id }),
	"owner_id": storekit.FilterID("owner_id", func(in *ListProjectsInput, id *ids.UserID) { in.OwnerID = id }),
	"phase":    storekit.FilterWord(func(in *ListProjectsInput, v *string) { in.Phase = v }),
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
