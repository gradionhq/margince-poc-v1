// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"testing"
	"time"
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

	if err := store.SaveReconcileWatermark(ctx, "contacts", newer); err != nil {
		t.Fatalf("save newer watermark: %v", err)
	}
	// An older save must be a no-op — not a regression.
	if err := store.SaveReconcileWatermark(ctx, "contacts", older); err != nil {
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
	if err := store.SaveReconcileWatermark(ctx, "contacts", newest); err != nil {
		t.Fatalf("save newest watermark: %v", err)
	}
	if got, _ := store.LoadReconcileWatermark(ctx, "contacts"); !got.Equal(newest) {
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

	if err := store.SaveBackfillCursor(ctx, "contacts", "", true); err != nil {
		t.Fatalf("save done cursor: %v", err)
	}
	// A stale done=false save must not re-open the converged backfill.
	if err := store.SaveBackfillCursor(ctx, "contacts", "cur-stale", false); err != nil {
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
