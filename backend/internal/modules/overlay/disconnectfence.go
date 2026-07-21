// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrConnectionGone is the sync-write fence's abort signal: a fenced write
// (MirrorStore.WithFence) found no active incumbent_connection for the
// workspace, so the connection has been revoked/disconnected since the sweep
// that issued the write began. The sweep treats it as a clean STOP, never a
// failure — there is nothing to sync into a workspace that has left overlay
// mode, and the revoked connection is already gone from the due-scan. It is
// exported so compose (the sweep orchestration) can recognize it, but it is
// deliberately NOT an apperrors sentinel: it is an application-internal
// control signal that never crosses an HTTP/MCP boundary — the on-demand
// reconcile path maps it to apperrors.ErrModeNotOverlay before it could.
var ErrConnectionGone = errors.New("overlay: the incumbent connection was revoked mid-sweep")

// assertActiveConnection is the disconnect-race fence. It takes a SHARED
// lock on the workspace's active incumbent_connection row for the calling
// transaction. Disconnect (teardown.go) takes that same row FOR UPDATE and,
// in the SAME transaction, purges every incumbent-derived table and flips
// the workspace back to native. The two lock modes make a fenced sync write
// and a disconnect mutually exclusive on that row, so an in-flight sweep
// write either:
//
//   - commits BEFORE the disconnect — its row is then purged by the
//     disconnect that was waiting on the shared lock; or
//   - runs AFTER the disconnect commits — it finds NO active connection row
//     and returns ErrConnectionGone, writing nothing.
//
// Either way a stray in-flight sweep can never resurrect incumbent-derived
// data into a disconnected workspace. overlay_mirror ingest is additionally
// tombstone-guarded in-SQL (mirrorstore.go's ingestSQL), but the association
// edges, the backfill cursor, the reconcile watermark, and mirror_user_map
// are not record-keyed and cannot be tombstoned — this fence is what
// protects THEM (and a brand-new mirror row that has no tombstone yet).
//
// The GUC is read WITHOUT missing_ok, exactly as lockWorkspaceVisibility is:
// a fenced write with app.workspace_id unset RAISEs rather than resolving to
// NULL and matching no row (or, worse, every row) — the same fail-closed
// posture the RLS policies take on the same condition. It is only ever
// reached inside database.WithWorkspaceTx, which sets the GUC.
func assertActiveConnection(ctx context.Context, tx pgx.Tx) error {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM incumbent_connection
		WHERE workspace_id = current_setting('app.workspace_id')::uuid
		  AND status = 'active'
		FOR SHARE`).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConnectionGone
	}
	if err != nil {
		return fmt.Errorf("overlay: asserting the active incumbent connection: %w", err)
	}
	return nil
}
