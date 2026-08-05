// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// isDue reports whether ws itself appears in DueOverlayConnections' fleet-
// wide scan. It tests MEMBERSHIP, never a count: this package's integration
// tests run in parallel against one shared clone database, so a concurrent
// test's own connection would inflate any len(due) assertion into a flaky
// one — filtering to ws is what keeps this deterministic.
func isDue(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ws ids.UUID) bool {
	t.Helper()
	due, err := DueOverlayConnections(ctx, pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	for _, d := range due {
		if d.Workspace.UUID == ws {
			return true
		}
	}
	return false
}

// TestSweepBackoffGatesDueOverlayConnections proves the backoff end to
// end: a freshly connected workspace is due; a connection-level failure
// backs it off so DueOverlayConnections stops selecting it (no more
// hot re-sweeping a dead/throttled connection); and one successful sweep
// resets the backoff so it is due again. now() is the real clock because
// the due-scan compares next_sweep_at against the DATABASE's now() — a
// backoff is always minutes in the future, a reset is always now-or-past,
// so the OUTCOME is deterministic without any sleep.
func TestSweepBackoffGatesDueOverlayConnections(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	if _, err := NewService(pool, vault, store).
		Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !isDue(ctx, t, pool, ws) {
		t.Fatal("a freshly connected workspace (no sync-state row) must be due immediately")
	}

	// A connection-level failure backs the sweep off into the future.
	if err := store.RecordSweepFailure(ctx, apperrors.ErrIncumbentBudgetExhausted, time.Now()); err != nil {
		t.Fatalf("RecordSweepFailure: %v", err)
	}
	if isDue(ctx, t, pool, ws) {
		t.Fatal("a backed-off workspace must NOT be due until next_sweep_at")
	}

	// One clean sweep resets the backoff — due again.
	if err := store.RecordSweepSuccess(ctx, time.Now()); err != nil {
		t.Fatalf("RecordSweepSuccess: %v", err)
	}
	if !isDue(ctx, t, pool, ws) {
		t.Fatal("after a successful sweep the workspace must be due again")
	}
}

// A sweep request makes a backed-off workspace due again, so the worker's
// due-gate picks it up on its next tick.
func TestRequestSweepMakesTheWorkspaceDueNow(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	if _, err := NewService(pool, vault, store).
		Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := store.RecordSweepFailure(ctx, errors.New("boom"), time.Now()); err != nil {
		t.Fatalf("RecordSweepFailure: %v", err)
	}
	if isDue(ctx, t, pool, ws) {
		t.Fatal("a just-failed connection is due immediately — the backoff did not apply")
	}

	if err := store.WithFence().RequestSweep(ctx); err != nil {
		t.Fatalf("RequestSweep: %v", err)
	}
	if !isDue(ctx, t, pool, ws) {
		t.Fatal("the requested sweep left the workspace undue")
	}
}

// A disconnected workspace's sync state stays a never-connected one's: a
// request racing a teardown must not repopulate what the purge removed.
func TestRequestSweepIsRefusedAfterDisconnect(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store)
	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if err := store.WithFence().RequestSweep(ctx); !errors.Is(err, ErrConnectionGone) {
		t.Fatalf("RequestSweep after disconnect = %v, want ErrConnectionGone", err)
	}

	var rows int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM overlay_sync_state`).Scan(&rows)
	}); err != nil {
		t.Fatalf("counting overlay_sync_state rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("overlay_sync_state has %d row(s) after a fenced RequestSweep on a disconnected workspace, want 0 — a request racing a teardown must not repopulate what the purge removed", rows)
	}
}

// TestRequestSweepObjectRBACDeniesReadOnlyAllowsAdmin is the deny/allow
// proof for the object-RBAC gate Service.RequestSweep carries (identity/
// internal/policy: overlay_connection is admin/ops-only for update, the
// same posture Connect/Disconnect already carry) — without it, any
// authenticated workspace member, even a read-only viewer, could fire an
// unbounded on-demand sweep request. Mirrors
// TestConnectionLifecycleObjectRBACDeniesMemberAllowsAdmin's shape
// (connection_integration_test.go).
// The deny and allow arms are one claim, not two: a deny-only test passes in a
// world where everything is denied, so the admin arm — a sweep that actually
// becomes due — is what makes the refusal mean something. Splitting the pure
// half into the unit lane would leave the remaining half unable to fail.
func TestRequestSweepObjectRBACDeniesReadOnlyAllowsAdmin(t *testing.T) {
	adminCtx, pool, ws := testWorkspaceCtx(t)
	_, memberUserID := testWorkspaceCtxAsUser(t, ws, "sweep-member@overlay.test")
	memberCtx := testMemberCtx(ws, memberUserID)
	vault := keyvault.NewMemory()
	svc := NewService(pool, vault, NewMirrorStore(pool, noOwnerEmails{}))

	if _, err := svc.Connect(adminCtx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A read-only member is denied — the object gate refuses the call
	// before it ever touches overlay_sync_state.
	if err := svc.RequestSweep(memberCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("member RequestSweep = %v, want apperrors.ErrPermissionDenied", err)
	}

	// An admin IS allowed, and the request leaves the workspace due.
	if err := svc.RequestSweep(adminCtx); err != nil {
		t.Fatalf("admin RequestSweep: %v", err)
	}
	if !isDue(adminCtx, t, pool, ws) {
		t.Error("an admin's RequestSweep must leave the workspace due")
	}
}
