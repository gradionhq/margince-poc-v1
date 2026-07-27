// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The one-time relocation of connector credentials off the legacy
// capture_connection.auth bytea column and into the keyvault, run on every
// boot until the column can be dropped. It is a raw relocation of bytes
// already in the tenant's own store, not a domain mutation: no record changes
// meaning, so it carries no audit or event.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// BackfillCredentials migrates every legacy capture_connection row whose
// credential still lives in the auth bytea column onto the vault: it seals the
// bytes, records the credential_ref, and clears auth. It is idempotent — a row
// that already carries a ref is skipped — so a re-run or a crash-retry is
// safe, which is what lets it run on every boot. It walks every live workspace
// under that workspace's own GUC, since capture_connection is RLS-scoped.
// One workspace's failure must not starve the rest of the fleet (the same
// invariant retention and the close-date sweep hold): the walk continues past
// a failing workspace and returns the count migrated plus the joined errors.
func (r *Registry) BackfillCredentials(ctx context.Context) (int, error) {
	if r.vault == nil {
		return 0, errors.New("capture: cannot backfill connector credentials without a keyvault")
	}
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return 0, fmt.Errorf("capture: listing workspaces for credential backfill: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return 0, err
	}
	total := 0
	var errs error
	for _, wsID := range workspaces {
		// The backfill's UPDATE runs under the workspace GUC only (a raw
		// relocation, not an audited domain write), so no actor/correlation
		// context is set — nothing here reads it.
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		migrated, err := r.backfillWorkspace(wsCtx, ids.From[ids.WorkspaceKind](wsID))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: backfilling workspace %s: %w", wsID, err))
			continue
		}
		total += migrated
	}
	return total, errs
}

// backfillWorkspace migrates one workspace's legacy rows. Each secret is
// sealed OUTSIDE the update tx (put-then-commit); the update then claims the
// row only if it still has no ref, so a concurrent backfill (two worker pods
// at boot) cannot double-migrate — the loser's sealed secret is a harmless
// orphan, never a corrupted row.
func (r *Registry) backfillWorkspace(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	type legacyRow struct {
		id   ids.UUID
		auth []byte
	}
	var pending []legacyRow
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, auth FROM capture_connection
			WHERE credential_ref IS NULL AND auth IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l legacyRow
			if err := rows.Scan(&l.id, &l.auth); err != nil {
				return err
			}
			pending = append(pending, l)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}

	migrated := 0
	for _, l := range pending {
		ref, err := r.vault.Put(ctx, ws, l.auth)
		if err != nil {
			return migrated, err
		}
		var claimed bool
		err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `
				UPDATE capture_connection SET credential_ref = $2, auth = NULL
				WHERE id = $1 AND credential_ref IS NULL`, l.id, string(ref))
			if err != nil {
				return err
			}
			claimed = ct.RowsAffected() == 1
			return nil
		})
		if err != nil {
			return migrated, err
		}
		if claimed {
			migrated++
		}
	}
	return migrated, nil
}
