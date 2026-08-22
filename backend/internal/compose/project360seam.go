// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seam behind read_project_360. It delegates to the SAME service the
// HTTP page is assembled by (compose/project360) rather than reimplementing
// any section: one assembly, read by two transports, so the tool and
// GET /projects/{id}/360 cannot disagree about what this caller may see.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/project360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// project360Reader binds the tool to the page's own service, built over the
// same field-catalog-bearing stores the handler set uses.
func project360Reader(pool *pgxpool.Pool) agents.Project360Reader {
	catalog := customfields.NewService(pool, nil)
	svc := project360.NewService(
		pool,
		deals.NewStore(InstallationDB(pool), DealsInstallation()).WithFieldCatalog(catalog),
		people.NewStore(InstallationDB(pool)).WithFieldCatalog(catalog),
		contracts.NewStore(InstallationDB(pool)),
		activities.NewStore(InstallationDB(pool)),
		clockNow,
	)
	return func(ctx context.Context, projectID ids.UUID) (crmcontracts.Project360, error) {
		return svc.Assemble(ctx, ids.From[ids.ProjectKind](projectID))
	}
}
