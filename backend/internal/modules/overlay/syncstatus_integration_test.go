// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// requireOverlayMode/Budget's own real-Postgres proof for the two mode-
// gate edge cases the compose e2e suite's happy path doesn't reach: a
// plain native-mode workspace (SyncStatus/Budget's honest 404) and an
// overlay-mode workspace whose Service was built with no budget meter
// wired (Budget's own "this is a wiring gap, not a mode question" error,
// distinct from ErrModeNotOverlay).

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// TestBackfillCompleteForRequiresEveryEngagementClass proves the plural
// translation's defining rule (OVA-MAP-1): "activity" is backed by all five
// engagement classes, so its backfill is complete ONLY when every one of the
// five cursors has converged — a single lagging class keeps it incomplete.
func TestBackfillCompleteForRequiresEveryEngagementClass(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, keyvault.NewMemory(), store).
		WithIncumbentClassesTranslator(func(canonical string) ([]string, bool) {
			if canonical == "activity" {
				return []string{"calls", "meetings", "emails", "notes", "tasks"}, true
			}
			return nil, false
		})

	completeInTx := func() bool {
		t.Helper()
		var complete bool
		if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
			var e error
			complete, e = svc.backfillCompleteFor(ctx, tx, "activity")
			return e
		}); err != nil {
			t.Fatalf("backfillCompleteFor: %v", err)
		}
		return complete
	}

	// Four of five engagement cursors converged; tasks still running.
	for _, class := range []string{"calls", "meetings", "emails", "notes"} {
		if err := store.SaveBackfillCursor(ctx, class, "", true); err != nil {
			t.Fatalf("seeding the %s cursor: %v", class, err)
		}
	}
	if err := store.SaveBackfillCursor(ctx, "tasks", "cur", false); err != nil {
		t.Fatalf("seeding the tasks cursor: %v", err)
	}
	if completeInTx() {
		t.Error("activity backfill reported complete while the tasks class is still running")
	}

	// The last class converges → activity is now complete.
	if err := store.SaveBackfillCursor(ctx, "tasks", "", true); err != nil {
		t.Fatalf("converging the tasks cursor: %v", err)
	}
	if !completeInTx() {
		t.Error("activity backfill reported incomplete after all five engagement cursors converged")
	}
}

func TestSyncStatusAndBudgetRefuseANativeModeWorkspace(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t) // never flips to overlay mode
	svc := NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})).
		WithBudgetMeter(NewMeter(pool, DefaultMeterConfig()))

	if _, err := svc.SyncStatus(ctx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("SyncStatus err = %v, want errors.Is(_, ErrModeNotOverlay)", err)
	}
	if _, err := svc.Budget(ctx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("Budget err = %v, want errors.Is(_, ErrModeNotOverlay)", err)
	}
}

func TestBudgetAnswersAWiringErrorWithNoMeterConfigured(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	svc := NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})) // no WithBudgetMeter

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-token"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	_, err := svc.Budget(ctx)
	if err == nil {
		t.Fatal("Budget with no meter configured: want an error, got nil")
	}
	if errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatal("Budget with no meter configured must not be mistaken for the mode gate — it is a wiring gap")
	}
}
