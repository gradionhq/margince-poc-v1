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
