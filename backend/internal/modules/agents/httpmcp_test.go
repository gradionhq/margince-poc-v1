// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// authenticateAsPassport builds an authenticate func for tests that need
// several distinct callers on the same handler: the request's
// X-Test-Passport header (test-only; no production client sends it)
// selects which passport the returned context carries.
func authenticateAsPassport(passports map[string]ids.UUID) func(*http.Request) (context.Context, error) {
	return func(r *http.Request) (context.Context, error) {
		id := passports[r.Header.Get("X-Test-Passport")]
		return principal.WithActor(context.Background(), principal.Principal{PassportID: id}), nil
	}
}

// deleteSession sends DELETE /mcp naming sessionID under asPassport (the
// X-Test-Passport header authenticateAsPassport reads) and returns the
// response status.
func deleteSession(t *testing.T, srv *httptest.Server, sessionID, asPassport string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("X-Test-Passport", asPassport)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	return resp.StatusCode
}

// sessionRegistered inspects h's registry directly — the ONLY place a
// DELETE's "closed" vs "never yours" outcome is actually distinguishable,
// since both answer 404 identically on the wire.
func sessionRegistered(t *testing.T, h http.Handler, passportID ids.UUID, sessionID string) bool {
	t.Helper()
	hh, ok := h.(*httpMCPHandler)
	if !ok {
		t.Fatalf("h is %T, want *httpMCPHandler", h)
	}
	hh.sessions.mu.Lock()
	defer hh.sessions.mu.Unlock()
	_, registered := hh.sessions.sessions[sessionKey{passportID: passportID, sessionID: sessionID}]
	return registered
}

// initialize mints a session and returns it as Mcp-Session-Id; DELETE
// closes it, but ONLY for the passport that opened it. The session id
// itself carries no authority (DESIGN §10.4) — a DELETE naming the right
// id under the WRONG passport must read exactly like "no such session"
// (404) and must not touch the registry entry it did not open, which this
// test checks directly rather than trusting the status code alone.
func TestInitializeReturnsASessionIDAndDeleteClosesOnlyYourOwn(t *testing.T) {
	passportA, passportB := ids.NewV7(), ids.NewV7()
	h := NewHTTPHandler(NewRegistry(nil, nil),
		authenticateAsPassport(map[string]ids.UUID{"a": passportA, "b": passportB}),
		func(*http.Request) string { return "" }, "margince-crm", "test")
	srv := httptest.NewServer(h)
	defer srv.Close()

	initReq, err := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	initReq.Header.Set("X-Test-Passport", "a")
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sid := initResp.Header.Get("Mcp-Session-Id")
	if err := initResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if sid == "" {
		t.Fatal("initialize did not return an Mcp-Session-Id header")
	}
	if !sessionRegistered(t, h, passportA, sid) {
		t.Fatalf("session %q was not registered under the initializing passport", sid)
	}

	// A DELETE naming the right session id but presenting a DIFFERENT
	// passport must fail exactly like an unknown id, and must not close
	// passport A's session.
	if status := deleteSession(t, srv, sid, "b"); status != http.StatusNotFound {
		t.Fatalf("DELETE by a different passport: status = %d, want 404", status)
	}
	if !sessionRegistered(t, h, passportA, sid) {
		t.Fatal("a DELETE from a different passport closed a session it does not own")
	}

	// The owning passport's DELETE succeeds and actually removes the entry.
	if status := deleteSession(t, srv, sid, "a"); status != http.StatusNoContent {
		t.Fatalf("DELETE by the owning passport: status = %d, want 204", status)
	}
	if sessionRegistered(t, h, passportA, sid) {
		t.Fatal("DELETE by the owning passport did not remove the session from the registry")
	}
}

// claude.ai calls these three right after initialize; this server
// legitimately has no resources or prompts, but answering -32601 method-
// not-found reads as a broken server rather than an empty, valid catalog.
func TestResourcesAndPromptsAnswerEmptyRatherThanMethodNotFound(t *testing.T) {
	s := NewStdioServer(NewRegistry(nil, nil), passthroughBind, "margince-crm", "test")
	for _, method := range []string{"resources/list", "resources/templates/list", "prompts/list"} {
		resp := s.handle(context.Background(), rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method,
		})
		if resp.Error != nil {
			t.Errorf("%s → error %d %q, want an empty result", method, resp.Error.Code, resp.Error.Message)
		}
	}
}

// Unauthenticated DELETE gets the identical 401 + RFC 9728 challenge as an
// unauthenticated POST — there is no teardown path that skips
// authentication.
func TestUnauthenticatedDeleteChallengesLikePost(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil),
		func(*http.Request) (context.Context, error) { return nil, errors.New("no token") },
		func(*http.Request) string {
			return `Bearer resource_metadata="https://crm.example.com/.well-known/oauth-protected-resource", scope="read draft"`
		}, "margince-crm", "test")

	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "whatever")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="https://`) {
		t.Errorf("challenge %q must carry the RFC 9728 pointer, same as POST", rec.Header().Get("WWW-Authenticate"))
	}
}

// DELETE with no Mcp-Session-Id header is a client error, not a 404 —
// there is nothing to look up.
func TestDeleteWithoutSessionHeaderIsBadRequest(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/mcp", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A client that asks for text/event-stream on POST gets a single `data:`
// frame carrying the JSON-RPC response, not the plain JSON body.
func TestPostWithEventStreamAcceptFramesASingleDataFrame(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test")
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("body %q does not start with a data: frame", body)
	}
	var frame rpcResponse
	payload := strings.TrimSuffix(strings.TrimPrefix(body, "data: "), "\n\n")
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		t.Fatalf("frame payload %q is not the JSON-RPC response: %v", payload, err)
	}
	if frame.Error != nil {
		t.Errorf("ping returned an error: %+v", frame.Error)
	}
}
