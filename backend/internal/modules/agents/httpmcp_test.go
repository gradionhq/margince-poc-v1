// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnauthenticatedRequestChallengesWithAnAbsolutePointerAndConservativeScope(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil),
		func(*http.Request) (context.Context, error) { return nil, errors.New("no token") },
		func(*http.Request) string {
			return `Bearer resource_metadata="https://crm.example.com/.well-known/oauth-protected-resource", scope="read draft"`
		}, "margince-crm", "test")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, `resource_metadata="https://`) {
		t.Errorf("challenge %q must carry an ABSOLUTE resource_metadata URL (RFC 9728)", got)
	}
	// Absent a scope hint Claude requests every scope we advertise, including
	// send. Naming read+draft makes the conservative grant the default.
	if !strings.Contains(got, `scope="read draft"`) {
		t.Errorf("challenge %q must carry the conservative scope hint", got)
	}
}

// TestResourceMetadataChallengeIsAbsoluteAndScopeBearing pins the builder the
// production mounts (compose's api edge and cmd/mcp) actually call — the test
// above only proves the handler forwards whatever challenge func it is given.
func TestResourceMetadataChallengeIsAbsoluteAndScopeBearing(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://crm.example.com/mcp", nil)
	// httptest.NewRequest never populates r.TLS, so RequestOrigin needs the
	// forwarded-proto signal a fronting proxy supplies in production to
	// resolve this as https rather than its http default.
	r.Header.Set("X-Forwarded-Proto", "https")

	got := ResourceMetadataChallenge(r)

	if !strings.Contains(got, `resource_metadata="https://crm.example.com/.well-known/oauth-protected-resource"`) {
		t.Errorf("challenge %q must carry an absolute resource_metadata URL on the request's own origin", got)
	}
	if !strings.Contains(got, `scope="read draft"`) {
		t.Errorf("challenge %q must carry the conservative scope hint", got)
	}
}
