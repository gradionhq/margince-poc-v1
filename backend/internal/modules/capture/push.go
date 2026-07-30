// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// BumpDueByMailbox routes a provider push notification onto the sweep: the
// notification names only a mailbox address, and the matching connection is
// found through the provider-owned identity the gmail connector writes into
// its own cursor (sync_cursor->>'email') — no credential is unsealed, no new
// column exists. Matching connections have their pacing clock zeroed so the
// next dispatch picks them up immediately; the returned pairs let the caller
// enqueue the sync jobs directly rather than waiting for the periodic scan.
//
// A mailbox nobody has connected matches nothing and returns empty — a push
// for a foreign address is a no-op, never an error (Pub/Sub redelivers on
// errors, and there is nothing here a retry would fix).
func BumpDueByMailbox(ctx context.Context, pool *pgxpool.Pool, provider, email string) ([]DueConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; the push carries no tenant, so every workspace is probed under its own GUC.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("capture: listing workspaces for the push walk: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}

	var hits []DueConnection
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		ws := ids.From[ids.WorkspaceKind](wsID)
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			// Upsert, not update: a connection that has never synced has no
			// sidecar row yet — a push for it must still land.
			rows, err := tx.Query(ctx, `
				INSERT INTO capture_sync_state (connection_id, workspace_id, next_sync_at)
				SELECT c.id, c.workspace_id, now()
				FROM capture_connection c
				WHERE c.provider = $1
				  AND c.status IN ('connected','error')
				  AND c.archived_at IS NULL
				  AND c.sync_cursor->>'email' = $2
				ON CONFLICT (connection_id) DO UPDATE SET next_sync_at = now()
				RETURNING connection_id`, provider, email)
			if err != nil {
				return err
			}
			matched, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
			if err != nil {
				return err
			}
			for _, id := range matched {
				hits = append(hits, DueConnection{Workspace: ws, ID: id})
			}
			return nil
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: push walk in workspace %s: %w", wsID, err))
		}
	}
	return hits, errs
}

// ResolveChannelConnection is Telegram ingress's version of the identical
// tenant-resolution problem BumpDueByMailbox solves for Gmail (design §6.1):
// the webhook carries no session, so before it can open ANY workspace
// transaction it must first learn which workspace owns the connection_id
// named in its path — and reading channel_connection to learn that is
// itself a tenant read. It enumerates workspaces from the un-scoped
// `workspace` table on the raw pool and probes each under its own GUC,
// exactly like the push walk, so no tenant identifier is ever taken from
// the caller — only a connection_id is, and that id is meaningless without
// a match.
//
// Only a `connected`, un-archived row matches. `pending` (registration not
// yet confirmed), `disconnected`, and archived (Disconnect archives on
// withdrawal) all read identically as "not found" — the second return is
// false — so an attacker probing connection ids over this unauthenticated
// edge learns nothing about which ones exist or what state they are in.
//
// Unlike the push walk, this stops at the first match and surfaces the
// first probing error immediately rather than accumulating across the whole
// fleet: id is a global primary key, so at most one workspace can ever hold
// it, and there is nothing to gain by continuing to probe workspaces that
// provably cannot.
func ResolveChannelConnection(ctx context.Context, pool *pgxpool.Pool, id ids.UUID) (ChannelConnection, bool, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; the webhook carries no tenant, so every workspace is probed under its own GUC.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL`)
	if err != nil {
		return ChannelConnection{}, false, fmt.Errorf("capture: listing workspaces for the channel-connection resolve: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return ChannelConnection{}, false, err
	}

	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		var conn ChannelConnection
		var secretRef string
		matched := false
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				SELECT `+channelConnectionColumns+`, webhook_secret_ref
				  FROM channel_connection
				 WHERE id = $1 AND status = $2 AND archived_at IS NULL`,
				id, channelStatusConnected)
			scanErr := row.Scan(&conn.ID, &conn.WorkspaceID, &conn.Provider, &conn.ChannelID, &conn.ChannelLabel,
				&conn.Status, &conn.Version, &conn.CreatedAt, &conn.UpdatedAt, &secretRef)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return nil // not in this workspace; the caller tries the next
			}
			if scanErr != nil {
				return scanErr
			}
			matched = true
			return nil
		})
		if err != nil {
			return ChannelConnection{}, false, fmt.Errorf("capture: channel-connection resolve in workspace %s: %w", wsID, err)
		}
		if matched {
			conn.WebhookSecretRef = keyvault.Ref(secretRef)
			return conn, true, nil
		}
	}
	return ChannelConnection{}, false, nil
}
