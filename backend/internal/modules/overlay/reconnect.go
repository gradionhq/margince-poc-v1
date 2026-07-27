// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns Connect's reconnect branch (connection.go's Connect calls
// into it when existingConnectionStatus finds a revoked row): reviving the
// workspace's revoked incumbent_connection row rather than inserting a new
// one, and the pre-flight read that tells Connect which of the two branches
// applies. Split out of connection.go (which keeps the fresh-insert path,
// Get, and the shared cleanupOrphanedRef both branches call) purely to stay
// under the file-length cap — a mechanical relocation of the
// reconnect-specific symbols, with no change to their logic. The write
// shape once the row itself is revived — Audit + Emit + the workspace mode
// flip, identical to what a fresh insert needs — is activateConnection
// (activation.go), not duplicated here.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reconnectConnection revives the workspace's revoked incumbent_connection
// row: the same UNIQUE(workspace_id) row, re-pointed at a freshly sealed
// credential and flipped back to active, in ONE transaction with the
// tombstone clear and activateConnection's audit + event + mode-flip (the
// write shape a fresh insert needs too, shared rather than repeated here).
//
// Clearing overlay_tombstone here is the point of the flow, not a cleanup
// detail: teardown deliberately leaves a tombstone per purged record so a
// stray in-flight sweep cannot resurrect it (purgeMirror), and only a NEW
// connection — a fresh trust decision by an admin — may mirror them again.
func (s *Service) reconnectConnection(ctx context.Context, in ConnectInput, ref keyvault.Ref, accountID string) (Connection, error) {
	var out Connection
	var supersededRef string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id ids.UUID
		var previousIncumbent, previousRegion string
		// The pre-read is FOR UPDATE so a concurrent reconnect serializes behind
		// it rather than both reviving the same row.
		if scanErr := tx.QueryRow(ctx, `
			SELECT id, incumbent, region, credential_ref FROM incumbent_connection
			WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			  AND status = 'revoked'
			FOR UPDATE`).Scan(&id, &previousIncumbent, &previousRegion, &supersededRef); scanErr != nil {
			return scanErr
		}

		var connectedAt time.Time
		if scanErr := tx.QueryRow(ctx, `
			UPDATE incumbent_connection SET
			  incumbent = $2, region = $3, credential_ref = $4, scopes = $5,
			  incumbent_account_id = NULLIF($6, ''),
			  status = 'active', connected_at = now(), revoked_at = NULL
			WHERE id = $1
			RETURNING connected_at`,
			id, in.Incumbent, in.Region, string(ref), leastPrivilegeHubSpotScopes, accountID,
		).Scan(&connectedAt); scanErr != nil {
			return scanErr
		}

		if _, delErr := tx.Exec(ctx, `DELETE FROM overlay_tombstone`); delErr != nil {
			return fmt.Errorf("overlay: clearing the teardown tombstones on reconnect: %w", delErr)
		}

		before := map[string]any{
			auditFieldIncumbent: previousIncumbent,
			auditFieldRegion:    previousRegion,
			auditFieldStatus:    statusRevoked,
		}
		activated, actErr := activateConnection(ctx, tx, id, in, connectedAt, "update", before)
		if actErr != nil {
			return actErr
		}
		out = activated
		return nil
	})
	if err != nil {
		// A concurrent reconnect already revived the row, so the FOR UPDATE
		// pre-read found no revoked row: the same lost-race outcome the insert
		// path answers, and the ref this attempt sealed is orphaned exactly the
		// same way.
		if errors.Is(err, pgx.ErrNoRows) {
			ws, ok := principal.WorkspaceID(ctx)
			if !ok {
				return Connection{}, apperrors.ErrIncumbentAlreadyConnected
			}
			return Connection{}, s.cleanupOrphanedRef(ctx, ws, ref)
		}
		return Connection{}, err
	}
	s.deleteSupersededRef(ctx, keyvault.Ref(supersededRef))
	return out, nil
}

// deleteSupersededRef removes the credential a reconnect replaced: the row now
// points at the new ref, so the old blob is unreferenced. It is the reconnect
// half of the post-commit cleanup deleteUnreferencedRef (connection.go) owns —
// see that function for why the delete outlives the request and why a failure
// is logged rather than returned. A workspace-less context cannot happen on
// this path (reconnectConnection only runs inside one) but is a no-op rather
// than a panic, since there is no workspace whose vault to reach into.
func (s *Service) deleteSupersededRef(ctx context.Context, ref keyvault.Ref) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return
	}
	s.deleteUnreferencedRef(ctx, ws, ref, "reconnect")
}

// existingConnectionStatus reports the workspace's incumbent_connection
// status, if it has a row at all. Connect's pre-flight distinguishes the two
// states the UNIQUE(workspace_id) row can be in: an active (or errored)
// connection refuses a second connect, while a revoked one — the residue
// Disconnect leaves so a stray in-flight sweep cannot resurrect a purged row —
// is what a reconnect revives.
func (s *Service) existingConnectionStatus(ctx context.Context) (status string, found bool, err error) {
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, `
			SELECT status FROM incumbent_connection
			WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`).Scan(&status)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		found = true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("overlay: checking for an existing incumbent connection: %w", err)
	}
	return status, found, nil
}
