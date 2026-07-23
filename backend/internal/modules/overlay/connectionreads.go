// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// Active-connection READS, split from connection.go (the Connect/Disconnect
// lifecycle): DueOverlayConnections is the poller's fleet-wide enumeration
// of every workspace with an active incumbent connection, and
// ActiveConnection is the per-request read of one workspace's own — both
// return the region + credential ref a caller needs to build a live
// incumbent adapter, without reaching into incumbent_connection's columns.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DueOverlayConnection names one active overlay incumbent connection to
// sweep — the poller's per-tenant enumeration unit (jobs.go's worker),
// mirroring capture.DueConnection
// (registry_connections.go): workspace + credential ref + region,
// everything the poller needs to build a live incumbent adapter without
// reaching into incumbent_connection's columns itself.
type DueOverlayConnection struct {
	Workspace     ids.WorkspaceID
	Incumbent     string
	Region        string
	CredentialRef keyvault.Ref
}

// DueOverlayConnections lists every workspace with an ACTIVE incumbent
// connection, fleet-wide — the same rls-exempt fleet-walk shape
// capture.Registry.DueConnections uses (workspace is not itself
// workspace-scoped, so this reads every tenant before entering each
// one's own GUC to read its own incumbent_connection row). One
// workspace's read failure is joined into the returned error but does
// not stop the rest of the fleet from being enumerated.
func DueOverlayConnections(ctx context.Context, pool *pgxpool.Pool) ([]DueOverlayConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL AND x_sor_mode = 'overlay' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("overlay: listing overlay-mode workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, fmt.Errorf("overlay: collecting overlay-mode workspace ids: %w", err)
	}

	var due []DueOverlayConnection
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		ws := ids.From[ids.WorkspaceKind](wsID)
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			var incumbent, region, ref string
			// The LEFT JOIN + next_sweep_at gate is the backoff (branch-1b):
			// a workspace whose last sweep failed carries an overlay_sync_state
			// row with a future next_sweep_at, so it is NOT selected until due
			// — no more re-sweeping a revoked/rate-limited/unreachable
			// connection hot every tick. No row (never swept, or reset by a
			// success) is due immediately (COALESCE to now()).
			scanErr := tx.QueryRow(wsCtx, `
				SELECT c.incumbent, c.region, c.credential_ref
				FROM incumbent_connection c
				LEFT JOIN overlay_sync_state s ON s.workspace_id = c.workspace_id
				WHERE c.status = $1 AND COALESCE(s.next_sweep_at, now()) <= now()`,
				statusActive).Scan(&incumbent, &region, &ref)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// Either x_sor_mode='overlay' with no active connection row (a
				// transient mid-teardown state), or an active connection that
				// is backed off and not yet due — in both cases the poller has
				// nothing to sweep for this workspace this tick, not an error.
				return nil
			}
			if scanErr != nil {
				return scanErr
			}
			due = append(due, DueOverlayConnection{
				Workspace: ws, Incumbent: incumbent, Region: region, CredentialRef: keyvault.Ref(ref),
			})
			return nil
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("overlay: reading the incumbent connection in workspace %s: %w", wsID, err))
		}
	}
	return due, errs
}

// WorkspaceForPortal resolves the workspace whose ACTIVE incumbent connection
// recorded incumbentAccountID (an inbound webhook's portalId) — the
// webhook-as-signal tenant binding (OVA-DDL-3, OVA-WIRE-10). A webhook carries
// no session/tenant, so this is the fleet-walk counterpart the receiver needs:
// it enumerates every overlay-mode workspace and probes each under its own GUC
// for an active connection carrying that portal (the same rls-exempt shape
// DueOverlayConnections uses — never a raw cross-tenant read). Fail-closed: a
// portal matching NO active connection returns apperrors.ErrNotFound, so the
// receiver rejects it and never ingests cross-tenant; a blank portal (a
// connection that recorded none yet) is likewise unbindable.
func WorkspaceForPortal(ctx context.Context, pool *pgxpool.Pool, incumbent, incumbentAccountID string) (ids.WorkspaceID, error) {
	if incumbentAccountID == "" {
		return ids.WorkspaceID{}, apperrors.ErrNotFound
	}
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC to probe its connection.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL AND x_sor_mode = 'overlay'`)
	if err != nil {
		return ids.WorkspaceID{}, fmt.Errorf("overlay: listing overlay-mode workspaces for portal binding: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return ids.WorkspaceID{}, fmt.Errorf("overlay: collecting workspace ids for portal binding: %w", err)
	}
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		var found bool
		if walkErr := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			var one int
			scanErr := tx.QueryRow(wsCtx, `
				SELECT 1 FROM incumbent_connection
				WHERE status = $1 AND incumbent = $2 AND incumbent_account_id = $3`,
				statusActive, incumbent, incumbentAccountID).Scan(&one)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return nil
			}
			if scanErr != nil {
				return scanErr
			}
			found = true
			return nil
		}); walkErr != nil {
			return ids.WorkspaceID{}, fmt.Errorf("overlay: probing workspace %s for portal binding: %w", wsID, walkErr)
		}
		if found {
			return ids.From[ids.WorkspaceKind](wsID), nil
		}
	}
	return ids.WorkspaceID{}, apperrors.ErrNotFound
}

// ActiveConnection reads ctx's workspace's ACTIVE incumbent connection —
// the per-request counterpart to DueOverlayConnections' fleet walk. The
// read path (FreshnessReader's live force-fresh resolver, wired in
// compose) uses it to build a live incumbent adapter for the request's
// own workspace. apperrors.ErrNotFound means the workspace has no active
// connection (never connected, mid-teardown, or disconnected) — the
// caller degrades to the mirror rather than treating it as an error.
func ActiveConnection(ctx context.Context, pool *pgxpool.Pool) (DueOverlayConnection, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return DueOverlayConnection{}, fmt.Errorf("overlay: active connection lookup requires a workspace-bound context")
	}
	var incumbent, region, ref string
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT incumbent, region, credential_ref FROM incumbent_connection
			WHERE status = $1`, statusActive).Scan(&incumbent, &region, &ref)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DueOverlayConnection{}, apperrors.ErrNotFound
		}
		return DueOverlayConnection{}, fmt.Errorf("overlay: reading the active incumbent connection: %w", err)
	}
	return DueOverlayConnection{
		Workspace:     ids.From[ids.WorkspaceKind](ws),
		Incumbent:     incumbent,
		Region:        region,
		CredentialRef: keyvault.Ref(ref),
	}, nil
}
