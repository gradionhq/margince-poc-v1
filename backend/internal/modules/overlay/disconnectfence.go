// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrConnectionGone is the sync-write fence's abort signal: a fenced write
// (MirrorStore.WithFence/WithFenceIdentity) found no active incumbent_connection
// for the workspace — OR, under WithFenceIdentity, found one active but under a
// DIFFERENT generation than the caller's own — so the connection this write
// believed it was serving is gone, either revoked or superseded by a reconnect,
// since the sweep that issued the write began. The sweep treats it as a clean
// STOP, never a failure — there is nothing to sync into a workspace that has
// left overlay mode (or that now belongs to a different connection this sweep
// never actually swept for), and the revoked connection is already gone from
// the due-scan. It is exported so compose (the sweep orchestration) can
// recognize it, but it is deliberately NOT an apperrors sentinel: it is an
// application-internal control signal that never crosses an HTTP/MCP
// boundary — the on-demand reconcile path maps it to apperrors.ErrModeNotOverlay
// before it could.
var ErrConnectionGone = errors.New("overlay: the incumbent connection was revoked mid-sweep")

// assertActiveConnection is the disconnect-race fence's STATUS-only form. It
// takes a SHARED lock on the workspace's active incumbent_connection row for
// the calling transaction. Disconnect (teardown.go) takes that same row FOR
// UPDATE and, in the SAME transaction, purges every incumbent-derived table
// and flips the workspace back to native. The two lock modes make a fenced
// sync write and a disconnect mutually exclusive on that row, so an in-flight
// write either:
//
//   - commits BEFORE the disconnect — its row is then purged by the
//     disconnect that was waiting on the shared lock; or
//   - runs AFTER the disconnect commits — it finds NO active connection row
//     and returns ErrConnectionGone, writing nothing.
//
// Either way a stray in-flight write can never resurrect incumbent-derived
// data into a DISCONNECTED workspace. overlay_mirror ingest is additionally
// tombstone-guarded in-SQL (mirrorstore.go's ingestSQL), but the association
// edges, mirror_user_map, and the sync-state backoff are not record-keyed and
// cannot be tombstoned — this fence is what protects THEM (and a brand-new
// mirror row that has no tombstone yet).
//
// KNOWN GAP — a RECONNECTED workspace is a different matter, and status alone
// does not cover it: it asks whether AN active connection exists, never
// whether it is the one the caller started under, and reconnectConnection
// revives the same row in place (status revoked→active, connected_at reset).
// A stray write from a caller that started under the PRIOR connection can
// still land after that reconnect commits. assertOwnConnection (below) closes
// this by checking the connection's IDENTITY too — MirrorStore.WithFenceIdentity
// engages it for every write the store makes, not only a named subset, so
// which check a given write gets is a property of how its STORE was built
// (WithFence vs WithFenceIdentity, assertFence's own doc), not of the write
// itself. WithFenceIdentity is what the periodic reconcile sweep and the
// webhook re-fetch worker use — both run unattended over a window long
// enough for a disconnect+reconnect to land mid-flight, and both already
// resolved the connection's identity (DueOverlayConnection.ConnectedAt /
// ActiveConnection's own read) before building their live incumbent adapter.
// A write-back or an on-demand sweep request (writeaudit.go, RequestSweep)
// stays on plain WithFence: its race window is one bounded HTTP request, not
// an unattended sweep, and closing it would need the shared per-request
// incumbent resolver (overlay.Provider.resolveIncumbent) to carry connectedAt
// too — a wider, unrelated seam change, tracked separately rather than
// bundled here.
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

// assertOwnConnection is assertActiveConnection plus the connection's
// IDENTITY: connectedAt is the value the caller resolved when it started
// (DueOverlayConnection.ConnectedAt / ActiveConnection's own read), and the
// predicate below requires the row active AND still carrying that same
// instant. A disconnect+reconnect resets connected_at (reconnect.go), so a
// write from a caller that straddles one gets ErrConnectionGone here exactly
// as it would from a disconnect alone — the write never LANDS under a
// connection the caller did not start under.
//
// Reached via MirrorStore.assertFence, never called directly outside it: a
// store built with WithFenceIdentity routes every fenced write through this
// instead of assertActiveConnection (assertActiveConnection's own doc names
// which callers choose which). SaveBackfillCursor/SaveReconcileWatermark
// (mirrorcheckpoints.go) are the one exception — they call this directly and
// UNCONDITIONALLY require connectedAt as an explicit parameter, never
// degrading to status-only, because a checkpoint (unlike a plain mirror row)
// is never revisited once it says done/advanced: landing one under the wrong
// generation would floor/short-circuit the new connection's own sync
// (ReconcileFloor) at a point it never actually reached — silently, and
// forever, since the watermark only advances and a done cursor is never
// re-listed.
func assertOwnConnection(ctx context.Context, tx pgx.Tx, connectedAt time.Time) error {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM incumbent_connection
		WHERE workspace_id = current_setting('app.workspace_id')::uuid
		  AND status = 'active'
		  AND connected_at = $1
		FOR SHARE`, connectedAt).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConnectionGone
	}
	if err != nil {
		return fmt.Errorf("overlay: asserting the incumbent connection's identity: %w", err)
	}
	return nil
}

// WithFenceIdentity is WithFence (mirrorstore.go) PLUS the connection's
// IDENTITY (assertFence then requires assertOwnConnection, not just
// assertActiveConnection): every fenced write additionally requires the
// active row to still carry connectedAt, so a write issued under an EARLIER
// connection generation is rejected even if the row is active again under a
// NEW one (a disconnect+reconnect straddling the caller's own work). The
// periodic reconcile sweep and the webhook re-fetch worker use this — both
// run unattended over a window long enough for a disconnect+reconnect to
// land mid-flight, and both already resolved connectedAt
// (DueOverlayConnection.ConnectedAt / ActiveConnection's own read) before
// building their live incumbent adapter, so passing it through here closes
// the race at no extra cost.
func (s *MirrorStore) WithFenceIdentity(connectedAt time.Time) *MirrorStore {
	c := *s
	c.fenced = true
	c.connectedAt = connectedAt
	return &c
}

// assertFence is the call every fenced write makes EXCEPT the two
// checkpoint saves (SaveBackfillCursor/SaveReconcileWatermark,
// mirrorcheckpoints.go — see assertOwnConnection's own doc for why those two
// call it directly and unconditionally instead): every other fenced method
// routes through assertFence rather than choosing between
// assertActiveConnection/assertOwnConnection itself, so upgrading a store
// from WithFence to WithFenceIdentity strengthens every one of THOSE writes
// without touching their bodies. A store built with WithFenceIdentity
// carries a non-zero connectedAt (incumbent_connection.connected_at is
// NOT NULL, so zero can only mean "never set"); one built with WithFence
// stays on the plain status check.
func (s *MirrorStore) assertFence(ctx context.Context, tx pgx.Tx) error {
	if !s.fenced {
		return nil
	}
	if !s.connectedAt.IsZero() {
		return assertOwnConnection(ctx, tx, s.connectedAt)
	}
	return assertActiveConnection(ctx, tx)
}
