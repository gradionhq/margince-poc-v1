// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fixedClock is the instant these tests mean by "now". A wall clock here would
// put the day boundary inside the assertion.
func fixedClock() time.Time {
	return time.Date(2026, 8, 21, 7, 30, 0, 0, time.UTC)
}

// idleReader is an agent at rest: it has nothing to report and no fault to
// report either, which is the case the empty-array contract exists for.
type idleReader struct{}

func (idleReader) Mine(context.Context, ids.UUID) ([]Item, []Item, error) {
	return nil, nil, nil
}

// signedIn is the request a resolved human makes.
func signedIn(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/agent-activity", nil)
	return r.WithContext(principal.WithActor(r.Context(), principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: ids.NewV7(),
	}))
}

// An anonymous request must not read one: the surface is defined by whose it is,
// so a caller with no identity has no feed rather than an empty one.
func TestAnUnidentifiedCallerIsRefusedRatherThanServedNothing(t *testing.T) {
	h := NewHandlers(nil, fixedClock)
	rec := httptest.NewRecorder()
	h.GetMyAgentActivity(rec, httptest.NewRequest(http.MethodGet, "/v1/me/agent-activity", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — an empty feed would read as an agent at rest", rec.Code)
	}
}

// An agent at rest answers with empty arrays, never with 404: "nothing is running"
// is a real answer, and a 404 would make the client show a fault.
func TestAnIdleAgentAnswersWithEmptyArrays(t *testing.T) {
	h := NewHandlers(idleReader{}, fixedClock)
	rec := httptest.NewRecorder()
	h.GetMyAgentActivity(rec, signedIn(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — an idle agent is an answer, not a fault", rec.Code)
	}
	// The raw body is asserted rather than a decoded struct: `null` and `[]`
	// both decode to a nil slice, and the difference between them is exactly
	// what breaks a client that iterates the array the contract promised.
	body := rec.Body.String()
	for _, want := range []string{`"running":[]`, `"recent":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s carries no %s — a null there crashes a client that iterates it", body, want)
		}
	}
	var wire struct {
		AsOf time.Time `json:"as_of"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("body is not the contract shape: %v", err)
	}
	if !wire.AsOf.Equal(fixedClock()) {
		t.Fatalf("as_of %s, want the injected instant %s", wire.AsOf, fixedClock())
	}
}
