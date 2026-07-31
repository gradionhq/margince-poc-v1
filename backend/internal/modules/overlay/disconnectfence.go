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
// A RECONNECTED workspace is a different matter from a disconnected one,
// and status alone does not cover it: it asks whether AN active connection
// exists, never whether it is the one the caller started under, and
// reconnectConnection revives the same row in place (status
// revoked→active, connected_at reset). A stray write from a caller that
// started under the PRIOR connection can still land after that reconnect
// commits. assertOwnConnection (below) closes this by checking the
// connection's IDENTITY too — see WithFenceIdentity's own doc
// (mirrorstore.go's fenced/identityFenced fields) for which callers engage
// it and why, and assertFence's for how a store's construction (WithFence
// vs WithFenceIdentity) — not the write itself — decides which check runs.
//
// The remaining gap is real and narrow: a write-back or an on-demand sweep
// request (writeaudit.go, RequestSweep) stays on plain WithFence, because
// closing it needs the shared per-request incumbent resolver
// (overlay.Provider.resolveIncumbent) to carry connectedAt, which it does
// not today. Concretely, a write-back straddling a disconnect+reconnect TO A
// DIFFERENT PORTAL can ingest portal A's record into portal B's mirror
// (intra-tenant data pollution, not a cross-tenant leak — the workspace
// scope never changes) and land a write-ledger echo entry under the new
// generation that could mask a genuine portal-B change as an echo.
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
// capture_connection.generation (an incrementing int, CAP-DDL-2) fences the
// same class of race for capture's connections; overlay reuses connected_at
// instead of adding a parallel generation column because it is already the
// exact value every caller needs anyway (reconcileFloor's own input,
// design.md's connect-instant), reset on every lifecycle change the same way
// a generation bump would be — a second counter would track the first one
// in lockstep for no added guarantee.
//
// Reached via MirrorStore.assertFence for every fenced write EXCEPT the two
// checkpoint saves (SaveBackfillCursor/SaveReconcileWatermark,
// mirrorcheckpoints.go), which call this directly instead of going through
// assertFence: those two take connectedAt as an explicit parameter — not
// s.connectedAt — because a checkpoint (unlike a plain mirror row) is never
// revisited once it says done/advanced, so its identity check must never be
// implicit in how the STORE happened to be built. Landing one under the
// wrong generation would floor/short-circuit the new connection's own sync
// (reconcile.go's reconcileFloor) at a point it never actually reached —
// silently, and forever, since the watermark only advances and a done
// cursor is never re-listed.
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

// WithFence returns a MirrorStore identical to s with the disconnect-race
// fence engaged on connection STATUS ALONE (assertFence routes it to
// assertActiveConnection — see the fenced field, mirrorstore.go): every sync
// write it issues aborts with ErrConnectionGone the moment the workspace is
// disconnected, instead of resurrecting purged incumbent-derived data. Use
// this for a fenced write whose window is one bounded request (a human
// write-back, an on-demand sweep request) — the disconnect+reconnect
// straddle this file's own doc describes is real here too, but its race
// window is one request, not an unattended background sweep, so
// WithFenceIdentity's stronger guarantee is not needed (see
// assertActiveConnection's own doc for why closing it here would need a
// wider seam change). It is opt-in precisely so the read path and the many
// unit tests that ingest without standing up a connection are not forced to
// hold one.
//
// It explicitly clears identityFenced/connectedAt rather than inheriting
// them from s: WithFence is the WEAKER of the two constructors, so
// s.WithFenceIdentity(x).WithFence() must actually downgrade to status-only,
// not silently stay identity-checked because the stronger fields survived
// the copy. No production caller chains them today, but the method's own
// name is the promise a future caller reads.
func (s *MirrorStore) WithFence() *MirrorStore {
	c := *s
	c.fenced = true
	c.identityFenced = false
	c.connectedAt = time.Time{}
	return &c
}

// WithFenceIdentity is WithFence PLUS the connection's IDENTITY (assertFence
// then requires assertOwnConnection, not just assertActiveConnection): every
// fenced write additionally requires the active row to still carry
// connectedAt, so a write issued under an EARLIER connection generation is
// rejected even if the row is active again under a NEW one (a
// disconnect+reconnect straddling the caller's own work). The periodic
// reconcile sweep and the webhook re-fetch worker use this — both run
// unattended over a window long enough for a disconnect+reconnect to land
// mid-flight, and both already resolved connectedAt
// (DueOverlayConnection.ConnectedAt / ActiveConnection's own read) before
// building their live incumbent adapter, so passing it through here closes
// the race at no extra cost.
func (s *MirrorStore) WithFenceIdentity(connectedAt time.Time) *MirrorStore {
	c := *s
	c.fenced = true
	c.identityFenced = true
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
// without touching their bodies.
//
// A store built with WithFenceIdentity MUST NOT silently downgrade to the
// weaker status-only check: identityFenced (mirrorstore.go) records that
// promise independently of connectedAt itself, so a WithFenceIdentity store
// that somehow carries a zero connectedAt (a caller bug — connected_at is
// NOT NULL, so a genuine read is never zero) fails CLOSED — but with
// errIdentityFenceMisconfigured, deliberately NOT ErrConnectionGone: the
// caller-bug case must surface as a real, logged, backed-off failure
// (reconcileConnection's callers already treat ErrConnectionGone as a
// benign clean stop with no backoff recorded, which would let a
// misconfigured store re-sweep hot forever instead of pacing off).
func (s *MirrorStore) assertFence(ctx context.Context, tx pgx.Tx) error {
	if !s.fenced {
		return nil
	}
	if err := assertMirrorUnfrozen(ctx, tx); err != nil {
		return err
	}
	if s.identityFenced {
		if s.connectedAt.IsZero() {
			return errIdentityFenceMisconfigured
		}
		return assertOwnConnection(ctx, tx, s.connectedAt)
	}
	return assertActiveConnection(ctx, tx)
}

// assertMirrorUnfrozen refuses every fenced mirror write while the flip
// preflight's seal holds (flipstate.go): the frozen snapshot the flip
// imports must not drift under it, and after a completed flip a late
// in-flight write-back must not reach the incumbent. It runs in the same
// transaction as the write it guards, so a seal committed before this
// read is visible to it.
func assertMirrorUnfrozen(ctx context.Context, tx pgx.Tx) error {
	var frozen bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM overlay_sync_state WHERE mirror_frozen_at IS NOT NULL)`,
	).Scan(&frozen); err != nil {
		return fmt.Errorf("overlay: checking the flip freeze before a fenced write: %w", err)
	}
	if frozen {
		return ErrMirrorFrozen
	}
	return nil
}

// errIdentityFenceMisconfigured is assertFence's fail-closed answer to a
// WithFenceIdentity store built with a zero connectedAt — a caller bug, not
// a real disconnect. It is deliberately NOT ErrConnectionGone: see
// assertFence's own doc for why conflating the two would silently drop the
// backoff a real failure needs.
var errIdentityFenceMisconfigured = errors.New("overlay: identity-fenced store built with a zero connectedAt; refusing the write")
