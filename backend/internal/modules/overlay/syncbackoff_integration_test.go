// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

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

	isDue := func() bool {
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

	if !isDue() {
		t.Fatal("a freshly connected workspace (no sync-state row) must be due immediately")
	}

	// A connection-level failure backs the sweep off into the future.
	if err := store.RecordSweepFailure(ctx, apperrors.ErrIncumbentBudgetExhausted, time.Now()); err != nil {
		t.Fatalf("RecordSweepFailure: %v", err)
	}
	if isDue() {
		t.Fatal("a backed-off workspace must NOT be due until next_sweep_at")
	}

	// One clean sweep resets the backoff — due again.
	if err := store.RecordSweepSuccess(ctx, time.Now()); err != nil {
		t.Fatalf("RecordSweepSuccess: %v", err)
	}
	if !isDue() {
		t.Fatal("after a successful sweep the workspace must be due again")
	}
}
