// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// budgetF32 dereferences a generated *float32 wire field, returning -1 for a
// nil pointer so a missing field fails a numeric assertion loudly.
func budgetF32(p *float32) float32 {
	if p == nil {
		return -1
	}
	return *p
}

// TestBudgetToWireExposesBreakdownHeadroomAndSearch is the OVB-AC-1 admin-surface
// contract test: the budget read carries the per-source breakdown (summing to
// consumed, OVB-AC-5), renders unattributable headroom as the `~unknown`
// sentinel — never a number (OVB-AC-1) — and includes the per-second Search
// window. An absent source (capture here) spent nothing and reads 0.
func TestBudgetToWireExposesBreakdownHeadroomAndSearch(t *testing.T) {
	w := budgetToWire(overlaybudget.Budget{
		Window: "24h", Consumed: 7, Limit: 10, Band: overlaybudget.BandWarn,
		Headroom: overlaybudget.UnknownHeadroom,
		Breakdown: map[overlaybudget.Source]int{
			overlaybudget.SourceForceFresh: 4,
			overlaybudget.SourcePoller:     3,
		},
		SearchWindow: "1s", SearchConsumed: 2, SearchLimit: 4, SearchBand: overlaybudget.BandOK,
	})

	if w.Headroom == nil || *w.Headroom != overlaybudget.UnknownHeadroom {
		t.Errorf("headroom = %v, want the %q sentinel (never a number, OVB-AC-1)", w.Headroom, overlaybudget.UnknownHeadroom)
	}
	if w.Sources == nil {
		t.Fatal("the budget read must carry the per-source breakdown (OVB-AC-1)")
	}
	ff, poller, capture := budgetF32(w.Sources.ForceFresh), budgetF32(w.Sources.Poller), budgetF32(w.Sources.Capture)
	if ff != 4 || poller != 3 || capture != 0 {
		t.Errorf("breakdown = force_fresh:%v poller:%v capture:%v, want 4/3/0", ff, poller, capture)
	}
	if sum := ff + poller + capture; sum != budgetF32(w.Consumed) {
		t.Errorf("breakdown sum = %v, want consumed %v (OVB-AC-5)", sum, budgetF32(w.Consumed))
	}
	if w.Search == nil || budgetF32(w.Search.Consumed) != 2 || budgetF32(w.Search.Limit) != 4 ||
		w.Search.Band == nil || *w.Search.Band != crmcontracts.OverlayBudgetSearchBandOk {
		t.Errorf("search window not carried through: %+v", w.Search)
	}
}

// fakeReconciler is a Reconciler stub that records whether it ran — the
// RBAC-deny proof needs to see it was NEVER invoked when the object gate
// refuses the caller, and the allow proof needs to see it WAS invoked
// when an admin/ops seat calls through.
type fakeReconciler struct {
	called bool
	err    error
}

func (f *fakeReconciler) Reconcile(_ context.Context) error {
	f.called = true
	return f.err
}

// requestAs builds a ReconcileOverlay request bound to a principal
// carrying the given overlay_connection grant — the same shape
// testsupport_integration.go's testWorkspaceCtx/testMemberCtx build, but
// hand-rolled here (no DB) since the object-RBAC gate itself
// (auth.Require) never touches a store or the database — only
// principal.Actor/principal.Permissions in ctx.
func requestAs(grant principal.ObjectGrant) *http.Request {
	ws := ids.NewV7()
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test-user", SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{overlayConnectionObject: grant},
			RowScope: principal.RowScopeAll,
		},
	})
	return httptest.NewRequest(http.MethodPost, "/v1/overlay/reconcile", nil).WithContext(ctx)
}

// TestReconcileOverlayObjectRBACDeniesReadOnlyAllowsAdmin is the deny/
// allow proof for the object-RBAC gate ReconcileOverlay must carry: a
// read-only (or any non-Update) seat triggering this live, budget-
// spending HubSpot sweep is the CRITICAL hole this test pins closed —
// without the gate, any authenticated workspace member (including
// read_only) could fire an unbounded live sweep against the incumbent
// on every request. Mirrors connection_integration_test.go's
// TestConnectionLifecycleObjectRBACDeniesMemberAllowsAdmin shape, but as
// a plain unit test: the gate itself never touches the database, only
// the ctx-bound principal.
func TestReconcileOverlayObjectRBACDeniesReadOnlyAllowsAdmin(t *testing.T) {
	t.Run("read-only seat is denied and the sweep never runs", func(t *testing.T) {
		reconciler := &fakeReconciler{}
		h := NewHandlers(nil).WithReconciler(reconciler)
		w := httptest.NewRecorder()

		h.ReconcileOverlay(w, requestAs(principal.ObjectGrant{Read: true}))

		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d (ErrPermissionDenied)", w.Code, http.StatusForbidden)
		}
		if reconciler.called {
			t.Error("reconciler.Reconcile was invoked despite the denied object grant — the sweep must never start before the gate passes")
		}
	})

	t.Run("admin seat (update grant) is allowed and the sweep runs", func(t *testing.T) {
		reconciler := &fakeReconciler{}
		h := NewHandlers(nil).WithReconciler(reconciler)
		w := httptest.NewRecorder()

		h.ReconcileOverlay(w, requestAs(principal.ObjectGrant{Read: true, Update: true}))

		if w.Code != http.StatusAccepted {
			t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
		if !reconciler.called {
			t.Error("reconciler.Reconcile was never invoked for an admin/ops-equivalent (update-granted) seat")
		}
	})
}

// TestReconcileOverlayMapsATimedOutSweepToServiceUnavailable proves the
// synchronous sweep's context.DeadlineExceeded is mapped to an honest
// 503, not swallowed or surfaced as an opaque 500 — the bound this task
// adds around the in-request HubSpot sweep.
func TestReconcileOverlayMapsATimedOutSweepToServiceUnavailable(t *testing.T) {
	reconciler := &fakeReconciler{err: context.DeadlineExceeded}
	h := NewHandlers(nil).WithReconciler(reconciler)
	w := httptest.NewRecorder()

	h.ReconcileOverlay(w, requestAs(principal.ObjectGrant{Read: true, Update: true}))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d for a timed-out sweep", w.Code, http.StatusServiceUnavailable)
	}
}

// TestReconcileOverlayMapsACanceledSweepToServiceUnavailable proves a
// caller-abandoned sweep (context.Canceled) is also an availability outcome
// (503, "cut off, retry"), not a misleading opaque 500 (A4b).
func TestReconcileOverlayMapsACanceledSweepToServiceUnavailable(t *testing.T) {
	reconciler := &fakeReconciler{err: context.Canceled}
	h := NewHandlers(nil).WithReconciler(reconciler)
	w := httptest.NewRecorder()

	h.ReconcileOverlay(w, requestAs(principal.ObjectGrant{Read: true, Update: true}))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d for a canceled sweep", w.Code, http.StatusServiceUnavailable)
	}
}

// TestReconcileOverlaySurfacesANonTimeoutReconcileError proves an
// ordinary reconcile failure (e.g. apperrors.ErrModeNotOverlay when the
// workspace has no active connection) still rides the normal sentinel
// mapping, unaffected by the new timeout branch.
func TestReconcileOverlaySurfacesANonTimeoutReconcileError(t *testing.T) {
	reconciler := &fakeReconciler{err: apperrors.ErrModeNotOverlay}
	h := NewHandlers(nil).WithReconciler(reconciler)
	w := httptest.NewRecorder()

	h.ReconcileOverlay(w, requestAs(principal.ObjectGrant{Read: true, Update: true}))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (ErrModeNotOverlay)", w.Code, http.StatusNotFound)
	}
	if !errors.Is(reconciler.err, apperrors.ErrModeNotOverlay) {
		t.Fatal("test setup drifted: reconciler.err is no longer ErrModeNotOverlay")
	}
}

// TestGetOverlayConnectionIsNotImplementedWithoutAService proves the
// zero-value-constructible posture handlers.go's own doc names: a
// Handlers built with no Service (h.svc==nil, e.g. a role that never
// called WithKeyvault) answers 501 rather than nil-derefing.
func TestGetOverlayConnectionIsNotImplementedWithoutAService(t *testing.T) {
	h := NewHandlers(nil)
	w := httptest.NewRecorder()
	h.GetOverlayConnection(w, httptest.NewRequest(http.MethodGet, "/overlay/connection", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d with no Service wired", w.Code, http.StatusNotImplemented)
	}
}

// TestPreflightAndExecuteOverlayFlipAreDeclared501 pins the flip pair's
// own doc contract: branch 2's read-mode->overlay flip stays an explicit
// 501 regardless of how Handlers was constructed — never a silent
// success or a nil-deref, the same "declared, not yet served" posture
// every other unimplemented op in this package takes.
func TestPreflightAndExecuteOverlayFlipAreDeclared501(t *testing.T) {
	h := NewHandlers(nil).WithReconciler(&fakeReconciler{})

	w := httptest.NewRecorder()
	h.PreflightOverlayFlip(w, httptest.NewRequest(http.MethodPost, "/overlay/flip/preflight", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("PreflightOverlayFlip status = %d, want %d", w.Code, http.StatusNotImplemented)
	}

	w = httptest.NewRecorder()
	h.ExecuteOverlayFlip(w, httptest.NewRequest(http.MethodPost, "/overlay/flip/execute", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("ExecuteOverlayFlip status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}
