// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// actingWorkspaceDB binds pool to the workspace the CALLER is acting in, for
// the few paths whose target tenant is not the installation's own.
//
// The overlay flip and its reconstruction are the whole list: a rebuild writes
// an exported estate into the workspace whose operator ordered it, which on a
// clean instance is a workspace the server never resolved. Everything else on a
// request path is the installation's one workspace and takes InstallationDB.
func actingWorkspaceDB(ctx context.Context, pool *pgxpool.Pool) (*database.DB, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: this call was made outside a workspace", database.ErrNoWorkspace)
	}
	return database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), nil
}
