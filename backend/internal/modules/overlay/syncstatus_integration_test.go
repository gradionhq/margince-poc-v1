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

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func TestSyncStatusAndBudgetRefuseANativeModeWorkspace(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t) // never flips to overlay mode
	svc := NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})).
		WithBudgetMeter(NewMeter(DefaultMeterConfig()))

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
