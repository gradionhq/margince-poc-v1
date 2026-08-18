// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The context a boot-time ledger write runs under.
//
// A boot step that records an operational fact — which extensions this binary
// composed, which release it was published as — writes to system_log like every
// other ledger row, which means it needs the three things every ledger write
// needs: the workspace the row belongs to, an actor to attribute it to, and a
// correlation scope. No request supplied any of them, because no request caused
// the write, so the boot has to bind them itself.
//
// Spelled once because the pre-bootstrap case is the subtle half and it is easy
// to get wrong in a way nothing notices: an installation with no organization
// yet has no workspace to record against, so there is nothing to write and
// nothing to compare — a boot step must treat that as "not yet", never as an
// error and never as an empty answer it then acts on.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// bootLedgerScope resolves the installation's workspace and binds the actor and
// correlation scope a system_log write needs, answering whether the
// installation is bootstrapped at all.
//
// A false second return is NOT an error: it is a pre-bootstrap installation, and
// the caller decides what "not yet" means for its own fact. The returned context
// is the input one in that case, so a caller that ignores the flag cannot
// accidentally write against an unbound workspace — database.WithWorkspaceTx
// answers ErrNoWorkspace instead.
func bootLedgerScope(ctx context.Context, pool *pgxpool.Pool, actor string) (context.Context, bool, error) {
	wsID, err := identity.NewService(pool).InstallationWorkspace(ctx)
	if errors.Is(err, identity.ErrNotBootstrapped) {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, err
	}
	ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: actor})
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return ctx, true, nil
}
