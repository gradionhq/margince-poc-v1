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

func authenticatedForTest(*http.Request) (context.Context, error) {
	return context.Background(), nil
}

// A present-and-unsupported MCP-Protocol-Version must be refused with a
// plain 400 whose body is prose, never a modern-era -32022 body — a
// -32022 body would tell a dual-era client this is a MODERN server, and it
// would retry with a handshake-free request this legacy server cannot
// serve, turning a working fallback into a hard failure.
func TestUnsupportedProtocolVersionHeaderIsRejectedWithPlain400(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "-32022") || strings.Contains(body, "UnsupportedProtocolVersionError") {
		t.Fatalf("body %q must not carry a modern-era -32022 error, or a dual-era client will retry a handshake this server does not serve", body)
	}
	for _, rev := range supportedProtocolVersions {
		if !strings.Contains(body, rev) {
			t.Errorf("body %q must name the supported revision %q", body, rev)
		}
	}
}

// Older clients never send the header at all; its absence must not block
// a request that would otherwise succeed. This exercises a real listener
// (not httptest.NewRecorder) because dispatch extends the write deadline via
// http.ResponseController, which the recorder's ResponseWriter does not
// implement.
func TestMissingProtocolVersionHeaderIsServedNormally(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test")
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// initialize negotiates the revision through its own request body, not this
// header — the header check must not fire on it, since a client that has
// not yet negotiated cannot be expected to already send a supported value.
func TestInitializeIsExemptFromTheProtocolVersionHeaderCheck(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test")
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (initialize negotiates via its own body, not the header)", resp.StatusCode)
	}
}
