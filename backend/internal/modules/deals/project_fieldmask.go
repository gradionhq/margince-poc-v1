// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project read mask, the sibling of fieldmask.go's deal mask and for the
// same reason: a projection that carries a reference to ANOTHER record must
// withhold the one its reader could not open, or the record becomes an
// existence oracle over rows that reader's own reads would refuse.
//
// A project has exactly one such reference — its anchor company. That company
// is capture-private until somebody promotes it (platform/auth rowscope.go),
// while the project hanging off it is read by every seat holding the object
// grant. So the two disagree by design, and this is where the wire answer is
// brought back in line with what the reader may actually see.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// filterOrganizationIDOnProject is masked_fields' name for the anchor
// reference. It is the wire member's own name, which is what a client reads
// the mask against.
const filterOrganizationIDOnProject = "organization_id"

// maskProjectForCaller applies the read mask to ONE row about to leave the
// store.
func maskProjectForCaller(ctx context.Context, tx pgx.Tx, p crmcontracts.Project) (crmcontracts.Project, error) {
	one := []crmcontracts.Project{p}
	if err := maskProjects(ctx, tx, one); err != nil {
		return crmcontracts.Project{}, err
	}
	return one[0], nil
}

// maskProjects withholds, per row, the anchor company this reader may not
// open. ONE statement answers the whole page, never a probe per row.
func maskProjects(ctx context.Context, tx pgx.Tx, projects []crmcontracts.Project) error {
	orgIDs := make([]ids.UUID, 0, len(projects))
	for _, p := range projects {
		if p.OrganizationId != nil {
			orgIDs = append(orgIDs, ids.UUID(*p.OrganizationId))
		}
	}
	// VisibleSubset answers an empty list without a round trip, and it checks
	// the organization object grant as well as the row scope — a seat holding
	// no `organization.read` learns no company id from a project either.
	visible, err := auth.VisibleSubset(ctx, tx, "organization", orgIDs)
	if err != nil {
		return err
	}
	for i := range projects {
		anchor := projects[i].OrganizationId
		if anchor == nil || visible[ids.UUID(*anchor)] {
			continue
		}
		projects[i].OrganizationId = nil
		named := []string{filterOrganizationIDOnProject}
		projects[i].MaskedFields = &named
	}
	return nil
}
