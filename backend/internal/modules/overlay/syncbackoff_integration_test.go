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
// wide scan. It tests MEMBERSHIP, never a count: the scan walks every
// workspace in the database, and the harness resets once per TEST rather than
// once per workspace (testsupport_integration.go), precisely because several
// tests in this package seed two. What else the database holds is therefore
// not this assertion's business — filtering to ws is what keeps it about ws.
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

// assertPacedWithin checks how far out the store actually paced the next
// sweep, measured by the database against its own clock. want is the delay the
// store intended; the observed remainder is at most that and at most one
// minute short of it, since the only thing between the pacing write and this
// read is the rest of the test.
//
// Due/not-due cannot see this. A ladder handed to Postgres in the wrong unit —
// make_interval(mins) where the code means seconds — reads as "not due" just
// like a correct one, and would sit there as a 60x backoff nobody measured.
func assertPacedWithin(ctx context.Context, t *testing.T, pool *pgxpool.Pool, want time.Duration) {
	t.Helper()
	var secs float64
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXTRACT(EPOCH FROM (next_sweep_at - now())) FROM overlay_sync_state`).Scan(&secs)
	}); err != nil {
		t.Fatalf("reading the paced next_sweep_at: %v", err)
	}
	got := time.Duration(secs * float64(time.Second))
	if got > want || got < want-time.Minute {
		t.Fatalf("next sweep paced %v out, want within [%v, %v]", got, want-time.Minute, want)
	}
}

// TestSweepBackoffGatesDueOverlayConnections proves the backoff end to
// end: a freshly connected workspace is due; a connection-level failure
// backs it off so DueOverlayConnections stops selecting it (no more
// hot re-sweeping a dead/throttled connection); and one successful sweep
// resets the backoff so it is due again. It needs no clock of its own and no
// sleep: the store schedules against the DATABASE's now() (syncbackoff.go) and
// the due-scan compares against that same now() (connectionreads.go), so a
// backoff is always minutes in the future and a reset is always now-or-past —
// whatever this process's clock happens to read. The pacing assertion covers
// the units of the interval the store hands Postgres, which no assertion on
// due/not-due can see: a ladder written in minutes instead of seconds still
// reads as "not due".
func TestSweepBackoffGatesDueOverlayConnections(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	if _, err := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), vault, store).
		Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !isDue(ctx, t, pool, ws) {
		t.Fatal("a freshly connected workspace (no sync-state row) must be due immediately")
	}

	// A connection-level failure backs the sweep off into the future.
	if err := store.RecordSweepFailure(ctx, apperrors.ErrIncumbentBudgetExhausted); err != nil {
		t.Fatalf("RecordSweepFailure: %v", err)
	}
	if isDue(ctx, t, pool, ws) {
		t.Fatal("a backed-off workspace must NOT be due until next_sweep_at")
	}
	assertPacedWithin(ctx, t, pool, rateLimitedFloor)

	// One clean sweep resets the backoff — due again.
	if err := store.RecordSweepSuccess(ctx); err != nil {
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
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	if _, err := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), vault, store).
		Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := store.RecordSweepFailure(ctx, errors.New("boom")); err != nil {
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
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	svc := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), vault, store)
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
//
// The deny and allow arms are one claim, not two: a deny-only test passes in a
// world where everything is denied, so the admin arm — a sweep that actually
// becomes due — is what makes the refusal mean something. Splitting the pure
// half into the unit lane would leave the remaining half unable to fail.
func TestRequestSweepObjectRBACDeniesReadOnlyAllowsAdmin(t *testing.T) {
	adminCtx, pool, ws := testWorkspaceCtx(t)
	_, memberUserID := testWorkspaceCtxAsUser(t, ws, "sweep-member@overlay.test")
	memberCtx := testMemberCtx(ws, memberUserID)
	vault := keyvault.NewMemory()
	svc := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), vault, NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{}))

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
