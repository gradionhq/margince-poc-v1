// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// fakeMode is an overlayModeChecker stub returning a fixed answer.
type fakeMode struct {
	overlay bool
	err     error
}

func (f fakeMode) isOverlay(context.Context) (bool, error) { return f.overlay, f.err }

// guardRequest builds a request carrying the chi route pattern the guard
// keys on — the same shape the contract router populates before running
// the middleware chain.
func guardRequest(method, pattern string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	r := httptest.NewRequest(method, "http://example.test"+pattern, nil)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestOverlayWriteGuard(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		pattern    string
		overlay    bool
		wantNext   bool
		wantStatus int
	}{
		{"SoR write refused in overlay", "POST", "/v1/people", true, false, http.StatusUnprocessableEntity},
		{"SoR write allowed off overlay", "POST", "/v1/people", false, true, http.StatusOK},
		{"deal advance refused in overlay", "POST", "/v1/deals/{id}/advance", true, false, http.StatusUnprocessableEntity},
		{"lead promote refused in overlay", "POST", "/v1/leads/{id}/promote", true, false, http.StatusUnprocessableEntity},
		{"archive refused in overlay", "DELETE", "/v1/people/{id}", true, false, http.StatusUnprocessableEntity},
		// Native governance write (human-only, e.g. an approval decision) is
		// NOT a SoR record write — it stays available in overlay.
		{"governance write allowed in overlay", "POST", "/v1/approvals/{id}/approve", true, true, http.StatusOK},
		// A read is never guarded.
		{"read passes through in overlay", "GET", "/v1/people", true, true, http.StatusOK},
		// An unknown route is not a SoR write — pass through.
		{"unknown route passes through", "POST", "/v1/not-a-route", true, true, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			h := overlayWriteGuard(fakeMode{overlay: tc.overlay})(next)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, guardRequest(tc.method, tc.pattern))

			if nextCalled != tc.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tc.wantNext)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
