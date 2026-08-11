// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// InstallationDB is the pool bound to the installation's own workspace
// (ADR-0091 §9 step 3): the handle a store uses instead of asking every caller
// to have put a workspace in ctx first.
//
// The resolution is identity's, because identity is the workspace authority —
// it is the module that refuses to serve when a second one exists (ADR-0061
// §3). Its resolver caches the first success, so the lookup happens once per
// process without this layer owning a variable that would make it a global.
//
// One Service per handle, deliberately: the cache lives on the Service, and
// sharing one across the api and the worker would make a bootstrap race in one
// role visible as a stale answer in the other.
func InstallationDB(pool *pgxpool.Pool) *database.DB {
	svc := identity.NewService(pool)
	return database.Bind(pool, svc.InstallationWorkspace)
}
