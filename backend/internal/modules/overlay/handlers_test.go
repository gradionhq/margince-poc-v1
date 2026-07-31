// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// budgetI64 dereferences a generated *int64 wire field, returning -1 for a nil
// pointer so a missing field fails a numeric assertion loudly.
func budgetI64(p *int64) int64 {
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
	ff, poller, capture := budgetI64(w.Sources.ForceFresh), budgetI64(w.Sources.Poller), budgetI64(w.Sources.Capture)
	if ff != 4 || poller != 3 || capture != 0 {
		t.Errorf("breakdown = force_fresh:%v poller:%v capture:%v, want 4/3/0", ff, poller, capture)
	}
	if sum := ff + poller + capture; sum != budgetI64(w.Consumed) {
		t.Errorf("breakdown sum = %v, want consumed %v (OVB-AC-5)", sum, budgetI64(w.Consumed))
	}
	if w.Search == nil || w.Search.Window == nil || *w.Search.Window != "1s" ||
		budgetI64(w.Search.Consumed) != 2 || budgetI64(w.Search.Limit) != 4 ||
		w.Search.Band == nil || *w.Search.Band != crmcontracts.OverlayBudgetBandOk {
		t.Errorf("search window not carried through: %+v", w.Search)
	}
}

// TestReconcileOverlayIsNotImplementedWithoutAService proves the
// zero-value-constructible posture handlers.go's own doc names: a
// Handlers built with no Service (h.svc==nil, e.g. a role that never
// called WithKeyvault) answers 501 rather than nil-derefing. The RBAC
// deny/allow proof and the "leaves the workspace due" proof for a wired
// Service live in syncbackoff_integration_test.go
// (TestRequestSweepObjectRBACDeniesReadOnlyAllowsAdmin) — RequestSweep
// touches Postgres, so that gate can no longer be exercised without one.
func TestReconcileOverlayIsNotImplementedWithoutAService(t *testing.T) {
	h := NewHandlers(nil)
	w := httptest.NewRecorder()
	h.ReconcileOverlay(w, httptest.NewRequest(http.MethodPost, "/overlay/reconcile", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d with no Service wired", w.Code, http.StatusNotImplemented)
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

// TestPreflightAndExecuteOverlayFlipAre501WhileUnwired pins the flip
// pair's fail-honest posture: with no FlipRunner injected (a role built
// without the flip's compose wiring) both ops answer an explicit 501 —
// never a silent success or a nil-deref, the same "declared, not
// served" posture every other unwired op in this package takes.
func TestPreflightAndExecuteOverlayFlipAre501WhileUnwired(t *testing.T) {
	h := NewHandlers(nil)

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
