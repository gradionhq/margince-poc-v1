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
// Spelled once because the pre-bootstrap case is the subtle half: an
// installation with no organization yet has no workspace to record against, so
// there is nothing to write and nothing to compare — a boot step must treat that
// as "not yet", never as an error and never as an empty answer it then acts on.
// The integration lane covers that arm for both facts.

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

// bootLedgerLock is the transaction-scoped advisory lock a boot observation takes
// before it reads the last one and decides whether to write. Every process role
// boots concurrently, and several api replicas boot at once during a rollout;
// without it each transaction reads the same previous observation and every one
// of them writes the change.
//
// ONE statement for both facts, parameterized by the fact's name — which is the
// half worth spelling once, because COALESCE is load-bearing in a way that fails
// silently. pg_advisory_xact_lock is strict: a NULL argument takes NO LOCK and
// returns NULL rather than raising. An unset app.workspace_id GUC makes
// current_setting(…, true) empty, `||` against NULL would be NULL, and the
// serialization this exists for would simply be absent — a guard that reports
// success while holding nothing. WithWorkspaceTx refuses an unbound transaction
// before any closure runs, so it is not reachable today; it is spelled this way
// because nothing downstream would say so if it became reachable, and because
// storekit already spells it this way.
const bootLedgerLock = `
	SELECT pg_advisory_xact_lock(
		hashtext($1 || coalesce(current_setting('app.workspace_id', true), ''))::bigint)`

// installationMarker identifies THIS installation inside a boot observation's
// detail payload.
//
// The ledgers lost their tenant column in ADR-0091 §8 phase D, and the boot
// facts are read back with "the newest row wins". That is right for an
// installation reading its own history and wrong for one carrying an archived
// predecessor's: the residue gate exempts the ledgers by name, because their
// immutability trigger makes clearing them impossible, so those rows are still
// there and the read can no longer tell them apart.
//
// So the observation says which installation made it. A row without the marker
// is not assumed to be ours — it predates this and may be a predecessor's — and
// the reader treats "no marked row" the same way it treats "nothing recorded":
// the comparison is disabled, which is the posture buildinfo already takes for
// an unstamped binary.
func installationMarker(ctx context.Context) string {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ""
	}
	return wsID.String()
}
