// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The sweep is the other half of what the payload posture promises: with
// trace_payloads on, this table holds correspondence content, and "24 hours"
// is only true if something deletes it.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSweepDeletesPastTheWindowAndKeepsWhatIsInside(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	seedTrace(memberCtx, t, db, me, "keep-me", time.Hour)
	seedTrace(memberCtx, t, db, me, "sweep-me", 25*time.Hour)

	removed, err := store.SweepOlderThan(memberCtx, 24*time.Hour)
	if err != nil {
		t.Fatalf("SweepOlderThan: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if got := traceRows(memberCtx, t, db, "sweep-me"); got != 0 {
		t.Errorf("rows for the aged message = %d, want 0 — the window is a deletion, not a filter", got)
	}
	if got := traceRows(memberCtx, t, db, "keep-me"); got != 1 {
		t.Errorf("rows inside the window = %d, want 1", got)
	}
}
