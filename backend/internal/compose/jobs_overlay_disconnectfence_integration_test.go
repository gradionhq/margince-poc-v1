// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The disconnect race, proved through the sweep orchestration against a real
// Postgres: a sweep that resolved its connection (and its token) before a
// disconnect — or a disconnect+reconnect — landed must stop cleanly and
// resurrect NOTHING into a workspace teardown has already flipped back to
// native. The fence itself is DB state rather than an incumbent response, so
// each case drives it by revoking/reviving the incumbent_connection row at the
// exact instant the race opens, then asserts both halves: ErrConnectionGone
// reaches the caller as the clean-stop signal, and every table teardown purged
// stays purged.

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep proves the
// disconnect-race fence end to end through the sweep orchestration: if the
// connection is revoked after the sweep resolved its token but before its
// writes land, reconcileConnection aborts with overlay.ErrConnectionGone —
// the clean-stop signal the worker turns into "skip this workspace, no
// backoff" — and resurrects nothing into the now-disconnected workspace.
func TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person" // canonical
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	due, err := overlay.DueOverlayConnections(adminCtx, e.Pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	var d overlay.DueOverlayConnection
	for _, c := range due {
		if c.Workspace.UUID == e.WS {
			d = c
		}
	}
	if d.Incumbent == "" {
		t.Fatal("no due overlay connection for the workspace after connect")
	}

	// Simulate a disconnect landing AFTER the sweep resolved its token: revoke
	// the connection row directly (leaving the vaulted token in place, so the
	// sweep's token resolution still succeeds and it proceeds to its first
	// fenced write, exactly the mid-sweep race the fence exists for).
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx,
			`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now()
			 WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("revoking the connection mid-sweep: %v", err)
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	err = reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc })
	if !errors.Is(err, overlay.ErrConnectionGone) {
		t.Fatalf("reconcileConnection over a revoked connection = %v, want overlay.ErrConnectionGone (clean stop)", err)
	}

	// The fenced sweep resurrected nothing: no mirror row, no owner mapping.
	var mirrorRows, userMaps int
	if qErr := database.WithWorkspaceTx(sweepCtx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(sweepCtx, `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows); err != nil {
			return err
		}
		return tx.QueryRow(sweepCtx, `SELECT count(*) FROM mirror_user_map`).Scan(&userMaps)
	}); qErr != nil {
		t.Fatalf("counting resurrected rows: %v", qErr)
	}
	if mirrorRows != 0 || userMaps != 0 {
		t.Errorf("after a fenced sweep over a revoked connection: overlay_mirror=%d mirror_user_map=%d, want 0/0 — the fence must resurrect nothing", mirrorRows, userMaps)
	}
}

// TestReconcileConnectionStopsCleanlyWhenReconnectedMidSweep proves the
// identity fence (overlay's assertOwnConnection/assertFence, disconnectfence.go
// and mirrorstore.go's WithFenceIdentity): a sweep that resolved its
// due-connection identity BEFORE a disconnect+reconnect straddles that race
// exactly like a mid-sweep disconnect alone — every fenced write it issues
// (SeedUserMap's UpsertUserMap, Ingest, the backfill checkpoint) aborts with
// ErrConnectionGone rather than landing under the NEW connection's identity.
// Before WithFenceIdentity existed, the status-only fence would have let all
// of these SUCCEED (an active row exists either way): a stray mirror row and
// owner mapping resurrected under the wrong generation, plus a done=true
// backfill cursor for a connection whose own backfill never actually ran —
// permanently short-circuiting it, since a done cursor is never re-listed
// and overlay.Reconcile's internal floor stops the incremental sweep from
// ever re-reading the gap.
func TestReconcileConnectionStopsCleanlyWhenReconnectedMidSweep(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person"
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	due, err := overlay.DueOverlayConnections(adminCtx, e.Pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	var d overlay.DueOverlayConnection
	for _, c := range due {
		if c.Workspace.UUID == e.WS {
			d = c
		}
	}
	if d.Incumbent == "" {
		t.Fatal("no due overlay connection for the workspace after connect")
	}

	// Simulate a disconnect+reconnect landing AFTER the sweep resolved its
	// due-connection identity (d, above) but BEFORE its first checkpoint
	// write: revive the SAME row with a fresh connected_at — exactly what
	// reconnectConnection does to the row's identity — via raw SQL rather
	// than the real Disconnect+Connect flow, so the sweep's already-resolved
	// vaulted token stays valid (the same reason the sibling mid-sweep
	// disconnect test above revokes via raw SQL instead of svc.Disconnect).
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx, `
			UPDATE incumbent_connection SET connected_at = now()
			WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("reconnecting mid-sweep: %v", err)
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	err = reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc })
	if !errors.Is(err, overlay.ErrConnectionGone) {
		t.Fatalf("reconcileConnection straddling a reconnect = %v, want overlay.ErrConnectionGone (the identity fence)", err)
	}

	// The new connection's own backfill cursor must NOT have been left
	// done=true by the straddling sweep's checkpoint write — that would
	// permanently short-circuit its real backfill (Backfill's own
	// top-of-function short-circuit, backfill.go).
	if _, done, loadErr := ms.LoadBackfillCursor(sweepCtx, overlay.IncumbentClassContacts); loadErr != nil {
		t.Fatalf("LoadBackfillCursor: %v", loadErr)
	} else if done {
		t.Error("the straddling sweep's checkpoint write must not land done=true for the new connection")
	}

	// The fenced sweep resurrected nothing else either: no mirror row, no
	// owner mapping — the same proof TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep
	// makes for a plain disconnect, now for a straddling reconnect.
	var mirrorRows, userMaps int
	if qErr := database.WithWorkspaceTx(sweepCtx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(sweepCtx, `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows); err != nil {
			return err
		}
		return tx.QueryRow(sweepCtx, `SELECT count(*) FROM mirror_user_map`).Scan(&userMaps)
	}); qErr != nil {
		t.Fatalf("counting resurrected rows: %v", qErr)
	}
	if mirrorRows != 0 || userMaps != 0 {
		t.Errorf("after a sweep straddling a reconnect: overlay_mirror=%d mirror_user_map=%d, want 0/0 — the identity fence must resurrect nothing under the new connection", mirrorRows, userMaps)
	}
}

// revokeOnOwnersIncumbent simulates a disconnect landing MID-SWEEP: it
// revokes the workspace's connection row (leaving the vaulted token in place)
// the first time the sweep calls Owners — after the due-scan enumerated the
// connection as active but before the sweep's first fenced write — then
// delegates to the wrapped fake. It is the deterministic hook that exercises
// the disconnect-race clean-stop paths (the fence itself is DB state, not an
// incumbent response, so it cannot be injected through the adapter directly).
type revokeOnOwnersIncumbent struct {
	overlay.Incumbent
	pool *pgxpool.Pool
	done bool
}

func (r *revokeOnOwnersIncumbent) Owners(ctx context.Context) ([]overlay.OwnerRef, error) {
	if !r.done {
		r.done = true
		if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
			_, execErr := tx.Exec(ctx,
				`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now()
				 WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
			return execErr
		}); err != nil {
			return nil, err
		}
	}
	return r.Incumbent.Owners(ctx)
}

// TestWorkerCleanStopsOnMidSweepDisconnect proves the worker's clean-stop: a
// connection revoked mid-sweep makes reconcileConnection return
// ErrConnectionGone, and Work skips the workspace WITHOUT recording a backoff
// or a success — so the overlay_sync_state row teardown purged is not
// resurrected, and nothing is re-mirrored into the now-native workspace.
func TestWorkerCleanStopsOnMidSweepDisconnect(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test") // matches Rep1, so SeedUserMap reaches a fenced UpsertUserMap

	w := &overlayReconcileWorkspaceWorker{
		pool: e.Pool, vault: vault, ms: ms,
		meter: workerBudgetMeter(t),
		log:   slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent {
			return &revokeOnOwnersIncumbent{Incumbent: fakeInc, pool: e.Pool}
		},
	}
	// A mid-sweep disconnect is a clean stop, not a failure: every fenced
	// write aborted, so there is nothing to retry and nothing to back off.
	if err := w.Work(e.Admin(), &river.Job[OverlayReconcileWorkspaceArgs]{
		Args: OverlayReconcileWorkspaceArgs{Workspace: e.WS},
	}); err != nil {
		t.Fatalf("a mid-sweep disconnect must not fail the job: %v", err)
	}

	// No sweep outcome was recorded: the clean-stop path skipped both
	// RecordSweepFailure and RecordSweepSuccess, so the purged
	// overlay_sync_state row stays gone (a resurrected row is exactly the P1
	// this fences).
	var syncStateRows, mirrorRows int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(e.Admin(), `SELECT count(*) FROM overlay_sync_state`).Scan(&syncStateRows); err != nil {
			return err
		}
		return tx.QueryRow(e.Admin(), `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows)
	}); err != nil {
		t.Fatalf("counting post-sweep rows: %v", err)
	}
	if syncStateRows != 0 {
		t.Errorf("overlay_sync_state has %d row(s) after a mid-sweep disconnect, want 0 — the clean stop must not resurrect the purged backoff row", syncStateRows)
	}
	if mirrorRows != 0 {
		t.Errorf("overlay_mirror has %d row(s) after a mid-sweep disconnect, want 0 — nothing may be re-mirrored into a now-native workspace", mirrorRows)
	}
}

// TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay reproduces the
// real race a TTL-caching mode dispatcher opens: another process's
// Disconnect can already have committed (connection revoked,
// overlay_sync_state purged, workspace flipped to native) while THIS
// process is still serving a stale cached "overlay" read. After a genuine
// Connect + Disconnect, it restores ONLY workspace.x_sor_mode/x_incumbent
// via raw SQL — never incumbent_connection or overlay_sync_state, which
// stay exactly as the teardown left them — so requireOverlayMode passes
// and RequestSweep is forced through to the fenced write instead of being
// turned away earlier by the mode gate. This is the real regression guard
// for two failure modes that race exposes: (1) RequestSweep must run
// against the FENCED store (MirrorStore.WithFence), or this stale-mode
// window would let it silently re-insert the overlay_sync_state row the
// teardown purged; (2) the fence's ErrConnectionGone must be mapped to
// apperrors.ErrModeNotOverlay before it can cross the wire, or this
// answers an opaque 500 instead. Deleting either one independently fails
// this test.
func TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	svc := overlay.NewService(e.Pool, vault, ms)
	adminCtx := overlayAdminCtx(e.WS, e.Rep1)

	if _, err := svc.Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := svc.Disconnect(adminCtx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Simulate the stale cached "overlay" mode read: restore ONLY
	// workspace.x_sor_mode/x_incumbent, leaving incumbent_connection
	// revoked and overlay_sync_state purged exactly as Disconnect left
	// them — so the mode gate passes and the call reaches the fence.
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx,
			`UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot'
			 WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("restoring the stale cached overlay mode: %v", err)
	}

	if err := svc.RequestSweep(adminCtx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("RequestSweep racing a disconnect = %v, want apperrors.ErrModeNotOverlay (not an opaque 500)", err)
	}

	var syncStateRows int
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(adminCtx, `SELECT count(*) FROM overlay_sync_state`).Scan(&syncStateRows)
	}); err != nil {
		t.Fatalf("counting overlay_sync_state rows: %v", err)
	}
	if syncStateRows != 0 {
		t.Errorf("overlay_sync_state has %d row(s) after a sweep request racing a disconnect, want 0 — the fence must not repopulate what the teardown purged", syncStateRows)
	}
}
