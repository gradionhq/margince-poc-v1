// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
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

// An outage is not a credential verdict. When the server cannot REACH a verdict
// the answer is 503 with no challenge: a 401 would tell a client its token is
// bad, and a well-behaved client then discards a good token and re-runs the
// whole OAuth dance against a server that is down.
func TestUnverifiableCredentialAnswers503RatherThanChallenging(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil),
		func(*http.Request) (context.Context, error) {
			return nil, fmt.Errorf("resolving the installation: %w: %w",
				errors.New("dial tcp 10.7.0.5:5432: connect: connection refused"), ErrAuthUnavailable)
		},
		func(*http.Request) string { return `Bearer resource_metadata="https://crm.example.com/x"` },
		"margince-crm", "test", discardLog())

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, "/mcp", strings.NewReader(`{}`)))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			// No challenge: a challenge is an instruction to re-authenticate,
			// which is exactly what must not happen during an outage.
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("503 carries a re-authentication challenge %q", got)
			}
			// The client is untrusted: the reason it cannot be verified stays
			// server-side.
			if body := rec.Body.String(); strings.Contains(body, "10.7.0.5") ||
				strings.Contains(body, "dial tcp") {
				t.Errorf("503 body leaked infrastructure detail: %q", body)
			}
		})
	}
}

// TestResourceMetadataChallengeIsAbsoluteAndScopeBearing pins the builder the
// production mount (compose's api edge) actually calls — the test above only
// proves the handler forwards whatever challenge func it is given.
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

// A handshake-era request naming a revision outside the compatibility window
// is refused with a 400 carrying -32022 and the list of revisions this server
// does serve. The list is what makes the refusal actionable: a client reads
// `supported` and retries on one of them instead of guessing, and both eras
// are in it because this server answers both.
//
// 2025-03-26 is in the table because it is the revision the window dropped
// (ADR-0092 §3) — but read what this actually proves. The header was
// introduced in 2025-06-18, so a genuine 2025-03-26 client never sends one;
// what is refused here is an implementation that knows the header and names a
// revision outside the window. A header-LESS request is still served, because
// the handshake-era revisions this server does serve only say a client SHOULD
// send it, and refusing every client that omits it would break the era this
// window exists to keep working.
func TestUnsupportedProtocolVersionHeaderIsRefusedWithTheSupportedList(t *testing.T) {
	h := NewHTTPHandler(NewRegistry(nil, nil), authenticatedForTest,
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())

	for _, requested := range []string{"1999-01-01", "2025-03-26"} {
		t.Run(requested, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
			req.Header.Set(headerProtocolVersion, requested)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var resp struct {
				Error struct {
					Code int `json:"code"`
					Data struct {
						Supported []string `json:"supported"`
						Requested string   `json:"requested"`
					} `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding %q: %v", rec.Body.String(), err)
			}
			if resp.Error.Code != codeUnsupportedProtocolVersion {
				t.Errorf("error code = %d, want %d", resp.Error.Code, codeUnsupportedProtocolVersion)
			}
			if resp.Error.Data.Requested != requested {
				t.Errorf("data.requested = %q, want %q", resp.Error.Data.Requested, requested)
			}
			if !slices.Equal(resp.Error.Data.Supported, supportedProtocolVersions()) {
				t.Errorf("data.supported = %v, want %v", resp.Error.Data.Supported, supportedProtocolVersions())
			}
		})
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

// A failed tool call is diagnosed server-side — a scrubbed cause has nowhere
// else to go, and a refusal's line is what tells an operator that a client is
// working from a stale tool list. That makes this transport's logger
// load-bearing: falling back to slog.Default() in a process that never called
// SetDefault — which cmd/api does not — writes those diagnostics to a handler
// nobody configured, in a format nobody is parsing.
func TestFailedToolCallsReachTheConfiguredLogger(t *testing.T) {
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

	// The client half: the invented name is the caller's own input, so quoting
	// it back discloses nothing — and it is how an agent tells its own mistake
	// from an outage it should wait out.
	if answer := string(body); !strings.Contains(answer, "unknown tool no_such_tool") ||
		strings.Contains(answer, "internal reason") {
		t.Errorf("the client answer %q does not name the invented tool as the caller's own mistake", answer)
	}
	// The server half, which is the point.
	if !strings.Contains(logged.String(), "mcp: tool call refused") || !strings.Contains(logged.String(), "no_such_tool") {
		t.Errorf("the configured logger recorded %q, want the refused call: the transport dispatched with a logger nobody configured", logged.String())
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

// ADR-0092 §6: nothing here holds per-connection state any more. `initialize`
// mints no session id, and DELETE answers 405 in BOTH framings with the same
// sentence.
//
// Written as an assertion about the WIRE rather than about the absent registry,
// because the registry's absence is not the property that matters — a
// reintroduced one would be invisible to a test that only checked the type. What
// matters is that no client is handed a session to pin itself to, which is what
// let a conversation land on any api replica.
func TestNoSessionIsMintedAndThereIsNoneToClose(t *testing.T) {
	passport := ids.NewV7()
	h := NewHTTPHandler(NewRegistry(nil, nil),
		authenticateAsPassport(map[string]ids.UUID{"a": passport}),
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
	if initResp.StatusCode != http.StatusOK {
		t.Fatalf("initialize: status = %d, want 200 — the handshake era still works", initResp.StatusCode)
	}
	if sid != "" {
		t.Errorf("initialize returned Mcp-Session-Id %q; this server establishes no session, "+
			"and a client handed one pins its conversation to whichever replica answered", sid)
	}

	// Both eras, one answer. A legacy client that still sends DELETE (it has no
	// id to name, but nothing stops it) is told the same thing a modern one is.
	for _, version := range []string{"", "2025-11-25", modernProtocolVersion} {
		req, err := http.NewRequest(http.MethodDelete, srv.URL, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("X-Test-Passport", "a")
		if version != "" {
			req.Header.Set(headerProtocolVersion, version)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE (%q): %v", version, err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("DELETE naming %q: status = %d, want 405", version, resp.StatusCode)
		}
		if !strings.Contains(string(body), "establishes no session") {
			t.Errorf("DELETE naming %q answered %q, which does not say why there is nothing to close", version, body)
		}
	}
}

// claude.ai calls these three right after initialize; this server
// legitimately has no resources or prompts, but answering -32601 method-
// not-found reads as a broken server rather than an empty, valid catalog.
func TestResourcesAndPromptsAnswerEmptyRatherThanMethodNotFound(t *testing.T) {
	s := NewDispatcher(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test")
	for _, method := range []string{"resources/list", "resources/templates/list", "prompts/list"} {
		resp := s.handle(context.Background(), rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method,
		}, legacyFraming)
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
	// The whole body, not one Read: a single Read may return a partial chunk,
	// which would fail the framing assertions below for a reason that has
	// nothing to do with framing. The handler writes one frame and returns, so
	// the stream ends and ReadAll terminates.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the SSE frame: %v", err)
	}
	body := string(raw)
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

// BYO-WIRE-1: the catalog this transport serves is cut to the presenting
// passport's scopes, so a shared cache holding one answer and replaying it to
// another credential discloses a surface that principal was never admitted to
// — and the replayed request never reaches the server, so nothing is audited.
//
// Both halves are asserted, and the first is why this is not a header test.
// Checking only the directive would pass just as well against a transport that
// had stopped filtering by scope altogether — the very state that makes the
// directive necessary. So the two answers must differ FIRST, and then both must
// refuse to be stored.
func TestTheScopeFilteredCatalogRefusesToBeStored(t *testing.T) {
	listing := func(t *testing.T, scopes ...principal.Scope) (tools []string, cacheControl string) {
		t.Helper()
		authenticate := func(*http.Request) (context.Context, error) {
			ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
			return principal.WithActor(ctx, principal.Principal{
				Type: principal.PrincipalAgent, ID: "agent:catalog", OnBehalfOf: ids.NewV7(),
				Scopes: principal.NewScopeSet(scopes...),
			}), nil
		}
		registry := NewRegistry(nil, nil)
		for name, scope := range map[string]principal.Scope{
			"read_tool":  principal.ScopeRead,
			"write_tool": principal.ScopeWrite,
		} {
			registry.Register(&fakeTool{spec: mcp.ToolSpec{
				Name: name, Title: name, Version: testToolVersion,
				Description:   name + " is offered to whoever holds its scope.",
				RequiredScope: scope, Tier: mcp.TierAutoExecute,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}})
		}
		h := NewHTTPHandler(registry, authenticate,
			func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
		// A real listener rather than a recorder: dispatch extends the write
		// deadline through http.ResponseController, which the recorder does
		// not implement.
		server := httptest.NewServer(h)
		t.Cleanup(server.Close)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatalf("building the tools/list request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Errorf("closing the response body: %v", err)
			}
		}()
		var decoded struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decoding the catalog: %v", err)
		}
		for _, tool := range decoded.Result.Tools {
			tools = append(tools, tool.Name)
		}
		return tools, resp.Header.Get("Cache-Control")
	}

	readOnly, readOnlyCache := listing(t, principal.ScopeRead)
	writing, writingCache := listing(t, principal.ScopeRead, principal.ScopeWrite)

	if len(readOnly) == 0 || len(writing) == 0 {
		t.Fatalf("a passport was served no tools at all (read %d, write %d) — this proves nothing about caching",
			len(readOnly), len(writing))
	}
	if slices.Equal(readOnly, writing) {
		t.Fatalf("both passports were served the same %d tools, so this response does not vary by scope — "+
			"either the filter is gone or this test no longer exercises it", len(readOnly))
	}
	for _, tc := range []struct {
		passport, got string
	}{{"read-only", readOnlyCache}, {"read+write", writingCache}} {
		if tc.got != "private, no-store" {
			t.Errorf("the %s passport's catalog was served with Cache-Control %q, want %q — a shared cache "+
				"may store it and answer another credential from it, and that request never reaches the server to be audited",
				tc.passport, tc.got, "private, no-store")
		}
	}
}
