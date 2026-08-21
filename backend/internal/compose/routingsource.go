// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Where the model binding comes from.
//
// It is a `setting` row, not a file read at boot. A binding an operator could
// only change by editing a file on the server and restarting both roles is one
// they change rarely and out of band — and in between, the api and the worker
// can be running different bindings with nothing saying so. In the database
// there is one answer and every role that asks gets the same one.
//
// The routing FILE keeps one job: seeding an installation that has no stored
// binding yet. That is what carries an existing deployment across the move
// without an operator doing anything, and it is why the file is read only when
// the setting is unset — an installation that has been provisioned once never
// consults it again, so a file left behind stale cannot quietly become the
// authority.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// routingSeedActor names the boot in the audit trail when a stored binding is
// planted from the deployment's routing file. It is a system actor because no
// human asked: the value was already this installation's, in a file.
const routingSeedActor = "routing-seed"

// ResolveRouting settles the binding this process runs on.
//
// An installation with a stored binding uses it and never opens the file. One
// with none adopts the file's binding, stores it, and reads the stored copy
// back — so what the Router runs on is always what the database holds, and a
// second role booting concurrently reaches the same value rather than its own
// copy of the file.
//
// A zero RoutingConfig means unconfigured, which is not an error: an
// installation that has bound no models runs with its AI lanes absent, exactly
// as one with no routing file did.
func ResolveRouting(ctx context.Context, pool *pgxpool.Pool, routingPath string, keys config.Lookup, log *slog.Logger) (ai.RoutingConfig, error) {
	ws, err := singletonWorkspace(ctx, pool)
	if err != nil {
		return ai.RoutingConfig{}, err
	}
	if ws == (ids.UUID{}) {
		// Unprovisioned (ADR-0105): the claim flow has not run, so there is no
		// installation to hold a binding. Serving is the point — every tenant
		// route answers 503 until it is claimed — so this is not a boot error.
		return ai.RoutingConfig{}, nil
	}
	ctx = routingCtx(ctx, ws)

	stored, err := readStoredRouting(ctx, pool)
	if err != nil {
		return ai.RoutingConfig{}, err
	}
	if stored.Unconfigured() && routingPath != "" {
		fromFile, err := ai.LoadRoutingFile(routingPath, keys)
		if err != nil {
			return ai.RoutingConfig{}, err
		}
		adopted, err := seedRouting(ctx, pool, fromFile)
		if err != nil {
			return ai.RoutingConfig{}, err
		}
		if !adopted {
			// A row appeared between the Unconfigured() read above and this
			// write — another replica booting at the same time. Saying "adopted"
			// would name a binding that is not the one now stored.
			log.Warn("the routing file was not adopted: a stored binding appeared while this boot was reading the file, and it wins",
				"file", routingPath)
		} else {
			log.Info("adopted the routing file as this installation's stored binding; it is authoritative now and the file is no longer read",
				"file", routingPath, "routing_version", fromFile.RoutingVersion())
		}
		// Re-read rather than returning what was just written. The seed is
		// ON CONFLICT DO NOTHING, so a role that lost the race to another one
		// stored nothing — and returning its own file copy would put two roles
		// on two bindings, which is the failure this move exists to end. There
		// is one return path for every case below, so no arm can hand back
		// something the database does not hold.
		if stored, err = readStoredRouting(ctx, pool); err != nil {
			return ai.RoutingConfig{}, err
		}
	}
	if stored.Unconfigured() {
		return ai.RoutingConfig{}, nil
	}
	return ai.FromStored(stored, keys)
}

// routingCtx binds the boot's system principal on the installation's
// workspace. A PrincipalSystem passes the object gate unconditionally, which is
// how the worker's sweeps read their settings too — the alternative would be an
// ungated read, and `setting` has no RLS beneath that gate.
func routingCtx(ctx context.Context, ws ids.UUID) context.Context {
	return bootCtx(ctx, ws, routingSeedActor)
}

// bootCtx is that same binding for any boot-time settings read: the workspace,
// a named system actor, and a correlation id, so a settings write made before
// any request exists is still attributable to the thing that made it.
func bootCtx(ctx context.Context, ws ids.UUID, actor string) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: actor,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

func readStoredRouting(ctx context.Context, pool *pgxpool.Pool) (ai.RoutingConfig, error) {
	return settings.Get(ctx, NewSettingsStore(pool), ai.Routing)
}

// seedRouting plants the file's binding, consumed exactly once: settings.Seed
// inserts only when no row exists, so a restart never overwrites a binding an
// admin has since changed.
//
// It answers whether the binding was STORED. "A restart" is the case the rule
// was written for; a stored row that predates this file is the case it is silent
// about, and an operator who edits ai-routing.yaml and sees no change deserves
// to be told which of the two happened.
func seedRouting(ctx context.Context, pool *pgxpool.Pool, cfg ai.RoutingConfig) (stored bool, err error) {
	err = database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		stored, err = settings.SeedValue(ctx, tx, ai.Routing, cfg)
		return err
	})
	return stored, err
}

// singletonWorkspace resolves the one live workspace this installation is
// (A107/ADR-0061), or the zero id when it has not been provisioned yet.
//
// A multi-workspace database is refused at boot by EnsureInstallation, which
// runs before this; taking the first here would be picking one of two answers
// to a question that must not have two.
func singletonWorkspace(ctx context.Context, pool *pgxpool.Pool) (ids.UUID, error) {
	live, err := enumerateWorkspaces(ctx, pool)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("compose: resolving the installation's workspace: %w", err)
	}
	if len(live) == 0 {
		return ids.UUID{}, nil
	}
	return live[0], nil
}
