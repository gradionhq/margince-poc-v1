// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// TestSaveReconcileWatermarkOnlyAdvances proves the watermark never moves
// backward (A4b): an older pass committing after a newer one — the periodic
// poller racing an on-demand reconcile — must not regress the checkpoint,
// which would re-sweep the window between and risk re-ingesting records the
// newer pass already saw.
func TestSaveReconcileWatermarkOnlyAdvances(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	newer := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	older := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	connectedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := store.SaveReconcileWatermark(ctx, "contacts", newer, connectedAt); err != nil {
		t.Fatalf("save newer watermark: %v", err)
	}
	// An older save must be a no-op — not a regression.
	if err := store.SaveReconcileWatermark(ctx, "contacts", older, connectedAt); err != nil {
		t.Fatalf("save older watermark: %v", err)
	}
	got, err := store.LoadReconcileWatermark(ctx, "contacts")
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if !got.Equal(newer) {
		t.Errorf("watermark = %v, want it to stay at the newer %v (an older pass must never move it back)", got, newer)
	}

	// A genuinely newer save still advances.
	newest := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	if err := store.SaveReconcileWatermark(ctx, "contacts", newest, connectedAt); err != nil {
		t.Fatalf("save newest watermark: %v", err)
	}
	got, err = store.LoadReconcileWatermark(ctx, "contacts")
	if err != nil {
		t.Fatalf("reload watermark after advance: %v", err)
	}
	if !got.Equal(newest) {
		t.Errorf("watermark = %v, want it to advance to %v", got, newest)
	}
}

// TestSaveBackfillCursorDoneIsSticky proves a converged backfill is never
// knocked back to pending (A4b): once done=true, an out-of-order save with
// done=false (a slower concurrent pass) must not re-open it, which would
// re-list the whole incumbent.
func TestSaveBackfillCursorDoneIsSticky(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	connectedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := store.SaveBackfillCursor(ctx, "contacts", "", BackfillProgress{Done: true}, connectedAt); err != nil {
		t.Fatalf("save done cursor: %v", err)
	}
	// A stale done=false save must not re-open the converged backfill.
	if err := store.SaveBackfillCursor(ctx, "contacts", "cur-stale", BackfillProgress{}, connectedAt); err != nil {
		t.Fatalf("save stale cursor: %v", err)
	}
	_, done, err := store.LoadBackfillCursor(ctx, "contacts")
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if !done {
		t.Error("backfill cursor done = false after a stale out-of-order save, want it to stay done=true (sticky)")
	}
}

// TestSaveBackfillCursorEnforcesIdentityEvenOnAPlainFenceStore proves
// SaveBackfillCursor/SaveReconcileWatermark's own identity check
// (assertOwnConnection) is unconditional — it fires even on a store built
// with the WEAKER WithFence (status-only), not just WithFenceIdentity. The
// checkpoint saves are the one exception to assertFence's degrade-to-status
// behavior (disconnectfence.go's own doc), and that exception needs its own
// proof: a store that never opted into identity-checking for its OTHER
// writes (Ingest, UpsertAssoc, ...) must still reject a checkpoint write
// carrying a stale connection identity.
func TestSaveBackfillCursorEnforcesIdentityEvenOnAPlainFenceStore(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store)

	conn, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-identity-secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	staleConnectedAt := conn.ConnectedAt

	// Simulate a reconnect: revive the row with a fresh connected_at, exactly
	// what reconnectConnection does to the row's identity — via raw SQL
	// rather than Disconnect+Connect, so this test isolates the checkpoint's
	// own identity check rather than re-exercising the reconnect flow.
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			UPDATE incumbent_connection SET connected_at = now()
			WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("reconnecting: %v", err)
	}

	// A plain WithFence store — status-only, NOT WithFenceIdentity — still
	// refuses a checkpoint write carrying the now-stale connectedAt.
	fenced := store.WithFence()
	if err := fenced.SaveBackfillCursor(ctx, "contacts", "", BackfillProgress{Done: true}, staleConnectedAt); !errors.Is(err, ErrConnectionGone) {
		t.Fatalf("SaveBackfillCursor with a stale connectedAt on a plain-WithFence store = %v, want ErrConnectionGone", err)
	}
	if err := fenced.SaveReconcileWatermark(ctx, "contacts", time.Now(), staleConnectedAt); !errors.Is(err, ErrConnectionGone) {
		t.Fatalf("SaveReconcileWatermark with a stale connectedAt on a plain-WithFence store = %v, want ErrConnectionGone", err)
	}

	// The CURRENT connection's identity still succeeds on the same store.
	var reconnected DueOverlayConnection
	if reconnected, err = ActiveConnection(ctx, pool); err != nil {
		t.Fatalf("ActiveConnection: %v", err)
	}
	if err := fenced.SaveBackfillCursor(ctx, "contacts", "", BackfillProgress{Done: true}, reconnected.ConnectedAt); err != nil {
		t.Fatalf("SaveBackfillCursor with the CURRENT connectedAt = %v, want success", err)
	}
}
