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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestBackfillCompleteForRequiresEveryEngagementClass proves the plural
// translation's defining rule (OVA-MAP-1): "activity" is backed by all five
// engagement classes, so its backfill is complete ONLY when every one of the
// five cursors has converged — a single lagging class keeps it incomplete.
func TestBackfillCompleteForRequiresEveryEngagementClass(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	svc := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), keyvault.NewMemory(), store).
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

	connectedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Four of five engagement cursors converged; tasks still running.
	for _, class := range []string{"calls", "meetings", "emails", "notes"} {
		if err := store.SaveBackfillCursor(ctx, class, "", BackfillProgress{Done: true}, connectedAt); err != nil {
			t.Fatalf("seeding the %s cursor: %v", class, err)
		}
	}
	if err := store.SaveBackfillCursor(ctx, "tasks", "cur", BackfillProgress{}, connectedAt); err != nil {
		t.Fatalf("seeding the tasks cursor: %v", err)
	}
	if completeInTx() {
		t.Error("activity backfill reported complete while the tasks class is still running")
	}

	// The last class converges → activity is now complete.
	if err := store.SaveBackfillCursor(ctx, "tasks", "", BackfillProgress{Done: true}, connectedAt); err != nil {
		t.Fatalf("converging the tasks cursor: %v", err)
	}
	if !completeInTx() {
		t.Error("activity backfill reported incomplete after all five engagement cursors converged")
	}
}

// TestSyncStatusShowsWhichClassHoldsAnOlderProjection is how an operator sees
// WHY the flip's force-fresh check will not clear: the same comparison the
// preflight aggregates into one verdict, reported per class on the surface
// that already exists — no new wire field. It also pins the two classes the
// comparison must spare, which is what keeps the verdict clearable at all:
// one whose declaration this deployment no longer holds, and one with no
// declared mapping left.
func TestSyncStatusShowsWhichClassHoldsAnOlderProjection(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	seedOverlayWorkspace(ctx, t, pool)
	db := database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
	store := NewMirrorStore(db, noOwnerEmails{})
	svc := NewService(db, keyvault.NewMemory(), store).
		WithIncumbentClassesTranslator(func(canonical string) ([]string, bool) {
			switch canonical {
			case "person":
				return []string{IncumbentClassContacts}, true
			case "organization":
				return []string{IncumbentClassCompanies}, true
			default:
				return nil, false
			}
		}).
		WithProjectionFingerprints(map[string]string{IncumbentClassContacts: "contacts-declaration-current"})

	baseline := time.Date(2026, 5, 13, 6, 44, 38, 0, time.UTC)
	for _, row := range []struct{ objectClass, ext, fingerprint string }{
		{"person", "p-legacy", "contacts-declaration-superseded"},
		// companies has no current declaration injected, and widget has no
		// mapping at all — neither class can be judged, so neither is stale.
		{"organization", "org-1", "companies-declaration-retired"},
		{"widget", "w-1", "widget-declaration-retired"},
	} {
		if err := store.Ingest(ctx, Record{
			ObjectClass: row.objectClass, ExternalID: row.ext,
			Fields:                map[string]any{"full_name": "Ingested Row"},
			ModifiedAt:            baseline,
			ProjectionFingerprint: row.fingerprint,
		}); err != nil {
			t.Fatalf("ingesting %s/%s: %v", row.objectClass, row.ext, err)
		}
	}

	statuses, err := svc.SyncStatus(ctx)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	states := make(map[string]string, len(statuses))
	for _, s := range statuses {
		states[s.Object] = s.State
	}
	want := map[string]string{
		"person":       syncStateStale,
		"organization": syncStateFresh,
		"widget":       syncStateFresh,
	}
	for object, wantState := range want {
		if states[object] != wantState {
			t.Errorf("sync state for %s = %q, want %q (all states: %v)", object, states[object], wantState, states)
		}
	}
}

func TestSyncStatusAndBudgetRefuseANativeModeWorkspace(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t) // never flips to overlay mode
	svc := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), keyvault.NewMemory(), NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})).
		WithBudgetMeter(overlaybudget.New(nil, nil))

	if _, err := svc.SyncStatus(ctx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("SyncStatus err = %v, want errors.Is(_, ErrModeNotOverlay)", err)
	}
	if _, err := svc.Budget(ctx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("Budget err = %v, want errors.Is(_, ErrModeNotOverlay)", err)
	}
}

func TestBudgetAnswersAWiringErrorWithNoMeterConfigured(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), keyvault.NewMemory(), NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})) // no WithBudgetMeter

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
