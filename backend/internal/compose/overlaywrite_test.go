// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
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
		// Archive of a mirrored type the provider DOES support
		// (overlay.SupportsWrite(WriteArchive, person) is true — archivableTypes)
		// is let through rather than refused: it is destined for the write
		// shadow, never the native handler.
		{"supported archive allowed in overlay", "DELETE", "/v1/people/{id}", true, true, http.StatusOK},
		// Archive of a mirrored type the provider does NOT support (activity
		// is not in archivableTypes) is still refused.
		{"unsupported archive refused in overlay", "DELETE", "/v1/activities/{id}", true, false, http.StatusUnprocessableEntity},
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

// runOverlayWriteGuard drives the guard for one request in overlay mode and
// reports whether it reached next and with what status — the shared shape
// TestOverlayWriteGuardAllowsNativeOnlyEntities and
// TestGuardRefusalMatchesProviderCapability both drive.
func runOverlayWriteGuard(method, pattern string) (nextCalled bool, status int) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	h := overlayWriteGuard(fakeMode{overlay: true})(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, guardRequest(method, pattern))
	return nextCalled, rec.Code
}

// A native-only entity is not mirrored, so its native table is the live one
// even in overlay mode: the guard must let its writes through rather than
// refusing on the tool verb alone (the bug this task fixes).
func TestOverlayWriteGuardAllowsNativeOnlyEntities(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		pattern string
	}{
		{"offer line-item create", "POST", "/v1/offers/{id}/line-items"},
		{"product update", "PATCH", "/v1/products/{id}"},
		{"tag create", "POST", "/v1/tags"},
		{"saved view archive", "DELETE", "/v1/views/{id}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled, status := runOverlayWriteGuard(tc.method, tc.pattern)
			if !nextCalled {
				t.Errorf("next called = false, want true (native-only entity must not be refused)")
			}
			if status != http.StatusOK {
				t.Errorf("status = %d, want %d", status, http.StatusOK)
			}
		})
	}
}

// The guard refuses exactly what the provider cannot serve — no more, no
// less. A verb/type the provider supports must reach its shadow (i.e. pass
// the guard); one it refuses must never reach a native handler.
func TestGuardRefusalMatchesProviderCapability(t *testing.T) {
	tests := []struct {
		entityType datasource.EntityType
		verb       overlay.WriteVerb
		method     string
		pattern    string
	}{
		{datasource.EntityPerson, overlay.WriteCreate, "POST", "/v1/people"},
		{datasource.EntityPerson, overlay.WriteUpdate, "PATCH", "/v1/people/{id}"},
		{datasource.EntityPerson, overlay.WriteArchive, "DELETE", "/v1/people/{id}"},
		{datasource.EntityOrganization, overlay.WriteCreate, "POST", "/v1/organizations"},
		{datasource.EntityOrganization, overlay.WriteUpdate, "PATCH", "/v1/organizations/{id}"},
		{datasource.EntityOrganization, overlay.WriteArchive, "DELETE", "/v1/organizations/{id}"},
		{datasource.EntityDeal, overlay.WriteCreate, "POST", "/v1/deals"},
		{datasource.EntityDeal, overlay.WriteUpdate, "PATCH", "/v1/deals/{id}"},
		{datasource.EntityDeal, overlay.WriteArchive, "DELETE", "/v1/deals/{id}"},
		{datasource.EntityLead, overlay.WriteCreate, "POST", "/v1/leads"},
		{datasource.EntityLead, overlay.WriteUpdate, "PATCH", "/v1/leads/{id}"},
		// Lead has no archive_record route — DELETE /v1/leads/{id} is
		// disqualify_lead, a lifecycle verb the seam mapping never carries
		// (overlayWriteVerbs), so it is covered by "lead promote refused in
		// overlay"'s sibling case above, not here.
		{datasource.EntityActivity, overlay.WriteCreate, "POST", "/v1/activities"}, // log_activity
		{datasource.EntityActivity, overlay.WriteUpdate, "PATCH", "/v1/activities/{id}"},
		{datasource.EntityActivity, overlay.WriteArchive, "DELETE", "/v1/activities/{id}"},
	}
	for _, tc := range tests {
		t.Run(string(tc.entityType)+"/"+string(tc.verb), func(t *testing.T) {
			nextCalled, status := runOverlayWriteGuard(tc.method, tc.pattern)
			wantNext := overlay.SupportsWrite(tc.verb, tc.entityType)
			if nextCalled != wantNext {
				t.Errorf("next called = %v, want %v (SupportsWrite(%s, %s) = %v)",
					nextCalled, wantNext, tc.verb, tc.entityType, wantNext)
			}
			wantStatus := http.StatusOK
			if !wantNext {
				wantStatus = http.StatusUnprocessableEntity
			}
			if status != wantStatus {
				t.Errorf("status = %d, want %d", status, wantStatus)
			}
		})
	}
}
