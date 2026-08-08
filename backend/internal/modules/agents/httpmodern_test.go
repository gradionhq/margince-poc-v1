// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The transport half of the modern framing: the headers a POST mirrors its
// body into, and the statuses that let a dual-era client tell which kind of
// server it reached.
//
// Every case here drives a real listener rather than httptest.NewRecorder,
// because dispatch extends the write deadline through http.ResponseController
// and the recorder's ResponseWriter does not implement it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// modernServer is the /mcp transport over one read tool, with the caller
// authenticated as an agent holding the read scope — the tool list and the
// call both need one, since an unauthenticated caller is shown nothing.
func modernServer(t *testing.T) *httptest.Server {
	t.Helper()
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	registry.Register(echoTool{
		spec: objectSpec("read_record", principal.ScopeRead),
		out:  json.RawMessage(`{"ok":true}`),
	})
	h := NewHTTPHandler(registry,
		func(r *http.Request) (context.Context, error) {
			return principal.WithActor(principal.WithWorkspaceID(r.Context(), ids.NewV7()),
				principal.Principal{
					Type: principal.PrincipalAgent, ID: "agent:modern", OnBehalfOf: ids.NewV7(),
					Scopes: principal.NewScopeSet(principal.ScopeRead),
				}), nil
		},
		func(*http.Request) string { return "" }, "margince-crm", "test", discardLog())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// modernPOST sends one modern request, letting a caller bend any header the
// mirroring contract is about.
func modernPOST(t *testing.T, srv *httptest.Server, body string, headers map[string]string) (int, map[string]json.RawMessage) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		if value == "" {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	var decoded map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decoding %q: %v", raw, err)
		}
	}
	return resp.StatusCode, decoded
}

// modernCallBody is a conforming tools/call, and modernCallHeaders the headers
// that mirror it. Every case below starts from this pair and breaks one thing.
const modernCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
	modernMetaJSON + `,"name":"read_record","arguments":{}}}`

func modernCallHeaders() map[string]string {
	return map[string]string{
		headerProtocolVersion: modernProtocolVersion,
		headerMethod:          "tools/call",
		headerName:            "read_record",
	}
}

// errorCode reads the JSON-RPC error code off a response, or fails when the
// response carries none.
func errorCode(t *testing.T, body map[string]json.RawMessage) int {
	t.Helper()
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body["error"], &rpcErr); err != nil {
		t.Fatalf("no JSON-RPC error in %v: %v", body, err)
	}
	return rpcErr.Code
}

// The headers exist so an intermediary can route without parsing the body,
// and the body is what this server executes. Every case here is that pair
// disagreeing — a gateway that allowed one tool while the server ran another,
// or a required header a router had nothing to read.
func TestAModernPostMustMirrorItsBodyIntoItsHeaders(t *testing.T) {
	srv := modernServer(t)
	for _, tc := range []struct {
		name  string
		bend  map[string]string
		wantK int
	}{
		{"the protocol version header is absent", map[string]string{headerProtocolVersion: ""}, codeHeaderMismatch},
		{
			"the protocol version header names another revision than the body",
			map[string]string{headerProtocolVersion: "2025-11-25"},
			codeHeaderMismatch,
		},
		{"the method header is absent", map[string]string{headerMethod: ""}, codeHeaderMismatch},
		{
			"the method header names another method than the body",
			map[string]string{headerMethod: "tools/list"},
			codeHeaderMismatch,
		},
		{"the name header is absent", map[string]string{headerName: ""}, codeHeaderMismatch},
		{
			"the name header names another TOOL than the body calls",
			map[string]string{headerName: "send_email"},
			codeHeaderMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := modernCallHeaders()
			for name, value := range tc.bend {
				headers[name] = value
			}

			status, body := modernPOST(t, srv, modernCallBody, headers)

			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if got := errorCode(t, body); got != tc.wantK {
				t.Errorf("code = %d, want %d", got, tc.wantK)
			}
			if _, answered := body["result"]; answered {
				t.Error("a refused request was answered as well as refused")
			}
		})
	}
}

// A conforming modern call is served, and it mints no session: a modern call
// carries its own state, so there is no id to hand back and nothing pinning
// the conversation to this replica.
func TestAConformingModernCallIsServedAndMintsNoSession(t *testing.T) {
	srv := modernServer(t)
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(modernCallBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for name, value := range modernCallHeaders() {
		req.Header.Set(name, value)
	}
	// A client that presented one anyway is ignored rather than echoed.
	req.Header.Set("Mcp-Session-Id", "01234567-89ab-cdef-0123-456789abcdef")

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
		refused, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("status = %d, and its body could not be read: %v", resp.StatusCode, err)
		}
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, refused)
	}
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.Errorf("Mcp-Session-Id = %q — a modern call establishes no session", got)
	}
}

// A name that cannot travel as plain ASCII arrives Base64-wrapped, and the
// server decodes before comparing. A value that only LOOKS like the sentinel
// is not a literal: clients must encode even a plain-ASCII value matching the
// pattern, so one that does not decode is a malformed header.
func TestAMirroredNameMayArriveBase64Encoded(t *testing.T) {
	srv := modernServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{` +
		modernMetaJSON + `,"uri":"margince://schema/query"}}`
	encoded := base64SentinelPrefix +
		base64.StdEncoding.EncodeToString([]byte("margince://schema/query")) + base64SentinelSuffix

	for _, tc := range []struct {
		name      string
		presented string
		wantCode  int
	}{
		// No provider is wired, so a mirrored name that MATCHES reaches the
		// dispatcher and earns the modern resource-not-found code. That it got
		// that far is the assertion: the header was accepted.
		{"decoded and matching", encoded, codeInvalidParams},
		{"decoded and naming another document", base64SentinelPrefix +
			base64.StdEncoding.EncodeToString([]byte("margince://schema/other")) + base64SentinelSuffix, codeHeaderMismatch},
		{"wrapped in the sentinel but not decodable", base64SentinelPrefix + "!!!not-base64!!!" + base64SentinelSuffix, codeHeaderMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, answered := modernPOST(t, srv, body, map[string]string{
				headerProtocolVersion: modernProtocolVersion,
				headerMethod:          "resources/read",
				headerName:            tc.presented,
			})

			if got := errorCode(t, answered); got != tc.wantCode {
				t.Errorf("code = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

// The statuses are how a dual-era client tells this server apart from a legacy
// one that does not host the endpoint at all: a modern refusal is a 4xx
// carrying a recognized JSON-RPC error, and anything else sends the client
// back to the handshake.
func TestModernRefusalsCarryTheStatusTheirClientReads(t *testing.T) {
	srv := modernServer(t)
	for _, tc := range []struct {
		name       string
		body       string
		headers    map[string]string
		wantStatus int
		wantCode   int
	}{
		{
			"a method this server does not answer",
			`{"jsonrpc":"2.0","id":1,"method":"tools/rename","params":{` + modernMetaJSON + `}}`,
			map[string]string{headerProtocolVersion: modernProtocolVersion, headerMethod: "tools/rename"},
			http.StatusNotFound, codeMethodNotFound,
		},
		{
			"a body that declares no version under a header that does",
			`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`,
			map[string]string{headerProtocolVersion: modernProtocolVersion, headerMethod: "ping"},
			http.StatusBadRequest, codeInvalidParams,
		},
		{
			"a version this server does not serve per request",
			`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{` +
				`"io.modelcontextprotocol/protocolVersion":"2099-01-01",` +
				`"io.modelcontextprotocol/clientCapabilities":{}}}}`,
			map[string]string{headerProtocolVersion: "2099-01-01", headerMethod: "ping"},
			http.StatusBadRequest, codeUnsupportedProtocolVersion,
		},
		{
			"the handshake era's own opening call, sent modern",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` + modernMetaJSON + `}}`,
			map[string]string{headerProtocolVersion: modernProtocolVersion, headerMethod: "initialize"},
			http.StatusNotFound, codeMethodNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := modernPOST(t, srv, tc.body, tc.headers)

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if got := errorCode(t, body); got != tc.wantCode {
				t.Errorf("code = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

// The handshake era is untouched by any of this: a legacy client still
// initializes, is answered a legacy revision, and is handed the session id it
// closes with DELETE.
func TestTheHandshakeEraStillOpensASession(t *testing.T) {
	srv := modernServer(t)
	req, err := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

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
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Error("initialize minted no session id — the handshake era still needs one")
	}
	var answered struct {
		Result struct {
			//nolint:tagliatelle // protocolVersion is the MCP wire member, camelCase by the protocol
			ProtocolVersion string `json:"protocolVersion"`
			//nolint:tagliatelle // resultType is the modern framing's member, absent here on purpose
			ResultType string `json:"resultType"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answered); err != nil {
		t.Fatalf("decoding the handshake: %v", err)
	}
	if answered.Result.ProtocolVersion != "2025-11-25" {
		t.Errorf("negotiated %q, want the revision the client asked for", answered.Result.ProtocolVersion)
	}
	if answered.Result.ResultType != "" {
		t.Errorf("a handshake result carries %q — that member belongs to the other era", answered.Result.ResultType)
	}
}

// decodeHeaderValue's own cases, including the two the wire makes ambiguous.
func TestDecodingAMirroredHeaderValue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		presented string
		want      string
		wantOK    bool
	}{
		{"a plain value passes through", "read_record", "read_record", true},
		{"a wrapped value is decoded", base64SentinelPrefix +
			base64.StdEncoding.EncodeToString([]byte("Hello, 世界")) + base64SentinelSuffix, "Hello, 世界", true},
		{"an unterminated sentinel is a plain value", "=?base64?read_record", "=?base64?read_record", true},
		{"a wrapped value that is not base64 cannot be read", base64SentinelPrefix + "%%%" + base64SentinelSuffix, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeHeaderValue(tc.presented)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("decoded = %q, want %q", got, tc.want)
			}
		})
	}
}

// A body that names nothing is not a body that agrees with a header. Each
// case here is a params shape that carries no readable name, and a header
// presented against any of them is a mismatch rather than a match against the
// empty string.
func TestABodyThatNamesNothingMatchesNoPresentedHeader(t *testing.T) {
	for _, tc := range []struct{ name, params string }{
		{"params are not an object", `[1,2]`},
		{"the member is absent", `{"arguments":{}}`},
		{"the member is not a string", `{"name":42}`},
		{"there are no params at all", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := json.RawMessage(tc.params)
			if tc.params == "" {
				params = nil
			}

			if got := paramsMember(params, "name"); got != "" {
				t.Fatalf("read %q out of a body that names nothing", got)
			}
			if refusal := validateMirroredName("read_record", params, "name"); refusal == nil {
				t.Error("a presented name matched a body that carries none")
			}
			// And with nothing presented either, the dispatcher gets to report
			// what is actually wrong with the body.
			if refusal := validateMirroredName("", params, "name"); refusal != nil {
				t.Errorf("refused %d %q, want the dispatcher to answer instead", refusal.Code, refusal.Message)
			}
		})
	}
}

// No refusal echoes the value it read back at the caller. The header is
// caller-controlled and its length is not, and naming the header and the
// member it contradicts is what a client author acts on anyway.
func TestAHeaderMismatchDoesNotEchoWhatTheCallerSent(t *testing.T) {
	srv := modernServer(t)
	headers := modernCallHeaders()
	headers[headerName] = strings.Repeat("A", 4096)

	_, body := modernPOST(t, srv, modernCallBody, headers)

	if strings.Contains(string(body["error"]), strings.Repeat("A", 32)) {
		t.Errorf("the refusal echoed the caller's own header value: %s", body["error"])
	}
	if !strings.Contains(string(body["error"]), headerName) {
		t.Errorf("the refusal %s must name the header that disagreed", body["error"])
	}
}
