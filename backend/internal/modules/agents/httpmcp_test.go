// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// discardLog is the logger for the cases that assert something other than what
// was logged: a handler built with the process default would write this
// package's diagnostics into the test output of every one of them.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestUnauthenticatedRequestChallengesWithAnAbsolutePointerAndConservativeScope(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil),
		func(*http.Request) (context.Context, error) { return nil, errors.New("no token") },
		func(*http.Request) string {
			return `Bearer resource_metadata="https://crm.example.com/.well-known/oauth-protected-resource", scope="read draft"`
		}, "margince-crm", "test", discardLog())

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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())

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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
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

// A tool failure outside the sentinel taxonomy is scrubbed on its way to the
// client by design, so the server-side log line is the ONLY place its cause
// survives. That makes the logger this transport dispatches with load-bearing:
// falling back to slog.Default() in a process that never called SetDefault —
// which cmd/api does not — writes the one diagnostic to a handler nobody
// configured, in a format nobody is parsing.
func TestScrubbedToolFailuresReachTheConfiguredLogger(t *testing.T) {
	var logged bytes.Buffer
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test",
		slog.New(slog.NewTextHandler(&logged, nil)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool"}}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	// The client half: generic, and it does not name the tool it could not find.
	if !strings.Contains(string(body), "internal reason") {
		t.Fatalf("the client answer %q is not the scrubbed one, so this proves nothing about what was logged instead", body)
	}
	// The server half, which is the point.
	if !strings.Contains(logged.String(), "mcp: tool call failed") || !strings.Contains(logged.String(), "no_such_tool") {
		t.Errorf("the configured logger recorded %q, want the cause of the scrubbed failure: the transport dispatched with a logger nobody configured", logged.String())
	}
}

// The registry is attacker-reachable state: `initialize` inserts an entry and
// only an exact-match DELETE ever removed one, so a client that never sends
// DELETE — a crash, a dropped connection, or a caller doing it on purpose —
// grew the map for the life of the process. These caps are what make it a
// bounded structure, and the eviction order is what makes the bound harmless:
// the session a client is actually using is the newest one.
func TestTheSessionRegistryIsBoundedPerPassportAndOverall(t *testing.T) {
	t.Run("one passport cannot exceed its own cap", func(t *testing.T) {
		registry := newSessionRegistry()
		passport := ids.NewV7()
		opened := make([]string, 0, maxSessionsPerPassport+1)
		for i := 0; i <= maxSessionsPerPassport; i++ {
			id := "session-" + strconv.Itoa(i)
			opened = append(opened, id)
			registry.register(passport, id)
		}

		if got := len(registry.sessions); got != maxSessionsPerPassport {
			t.Errorf("registry holds %d sessions for one passport, want the cap of %d", got, maxSessionsPerPassport)
		}
		// The oldest gave way, and the newest — the one the client is using —
		// is the one that survived.
		if registry.close(passport, opened[0]) {
			t.Error("the oldest session survived, so the cap evicted something else")
		}
		if !registry.close(passport, opened[len(opened)-1]) {
			t.Error("the newest session was evicted: a client's live session must outlive its abandoned ones")
		}
	})

	t.Run("the whole registry is bounded across passports", func(t *testing.T) {
		registry := newSessionRegistry()
		// The per-passport cap alone leaves the PASSPORT dimension unbounded:
		// every refresh rotation mints a fresh one, each with its own
		// allowance, so a long-lived connection walks through credentials.
		for i := 0; i <= maxSessions; i++ {
			registry.register(ids.NewV7(), "session-"+strconv.Itoa(i))
		}
		if got := len(registry.sessions); got > maxSessions {
			t.Errorf("registry holds %d sessions, want at most the cap of %d", got, maxSessions)
		}
	})

	t.Run("one passport cannot evict another's sessions", func(t *testing.T) {
		registry := newSessionRegistry()
		innocent, noisy := ids.NewV7(), ids.NewV7()
		registry.register(innocent, "innocent-session")
		for i := 0; i < maxSessions; i++ {
			registry.register(noisy, "noisy-"+strconv.Itoa(i))
		}

		if got := len(registry.sessions); got > maxSessionsPerPassport+1 {
			t.Errorf("registry holds %d sessions, want one passport bounded to %d plus the other's one", got, maxSessionsPerPassport)
		}
		if !registry.close(innocent, "innocent-session") {
			t.Error("a flood of sessions under one passport closed another passport's session")
		}
	})
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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
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
	s := NewStdioServer(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test")
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
		}, "margince-crm", "test", discardLog())

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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())

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
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
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
