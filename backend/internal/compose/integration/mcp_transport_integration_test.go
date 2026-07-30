// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The remote MCP connector as a third-party client meets it: over the
// COMPOSED mux, so what is asserted is the route set a real client reaches.
// Two properties, and they are opposites of each other — with the deployment
// gate on, the RFC 9728 discovery chain closes on one origin (the 401 names
// an absolute metadata URL, that URL is served here, and it points back at
// this issuer); with the gate off, every route in the group is simply absent.
//
// This file also carries the harness and the wire helpers the sibling
// connector suites share: mcp_handshake_integration_test.go walks the whole
// client bootstrap on one origin, and mcp_deadline_integration_test.go proves
// a tool call outlives the server's own response deadline.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// connectorEnv is the harness for the remote connector: the composed api
// handler with the connector gate on, plus the origin it is reachable at.
// The origin is a field rather than a constant because the discovery
// documents carry ABSOLUTE URLs — an assertion about what a client
// dereferences cannot hardcode a port the OS assigns.
type connectorEnv struct {
	*env
	origin string
}

// setupConnector boots the harness with the connector enabled. It goes
// through compose.New — the same mux cmd/api serves — because a hand-rolled
// handler can pass while the real route set is broken: the 401's pointer,
// the discovery documents and the transport only form a chain if one mux
// serves all three.
func setupConnector(t *testing.T) *connectorEnv {
	t.Helper()
	return setupConnectorWith(t)
}

// setupConnectorWith is setupConnector plus whatever else one test needs
// wired. It is separate so setupConnector stays the plain deployment posture
// — connector on, nothing else declared — which is what most of this suite
// asserts against.
func setupConnectorWith(t *testing.T, extra ...compose.Option) *connectorEnv {
	t.Helper()
	e := setupWithOriginOptions(t, func(origin string) []compose.Option {
		// The advertised resource comes from configuration, exactly as
		// --public-base-url supplies it in a deployment — never from the
		// Host header, which an attacker controls.
		return append([]compose.Option{
			compose.WithMCPConnector(), compose.WithMCPResource(origin + "/mcp"),
		}, extra...)
	})
	e.slug = "mcp-connector" // slugify("MCP Connector")
	bootstrapWorkspaceSession(t, e, "MCP Connector", "granter@fable.test", "Admin")
	return &connectorEnv{env: e, origin: e.ts.URL}
}

// httpResult is one exchange's outcome with the body already drained and
// closed, so a caller asserts on it without owning the response lifecycle.
type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       string
}

// listTools is the plainest exchange on the transport and the one every
// admission assertion needs: whether this credential gets in at all. An empty
// bearer sends NO Authorization header — the unauthenticated shape a client
// starts its discovery from, which is not the same as an empty credential.
func (e *env) listTools(t *testing.T, bearer string) httpResult {
	t.Helper()
	headers := map[string]string{"Content-Type": "application/json"}
	if bearer != "" {
		headers["Authorization"] = "Bearer " + bearer
	}
	return e.raw(t, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, headers)
}

// getJSON dereferences a discovery document the way a client does and fails
// on anything but a 200 with a JSON object.
//
//craft:ignore naked-any a discovery document is an open JSON object by RFC 8414/9728 — asserting on it means reading it untyped
func (e *env) getJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	got := e.raw(t, http.MethodGet, path, "", nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d %s", path, got.StatusCode, got.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got.Body), &doc); err != nil {
		t.Fatalf("GET %s: body is not a JSON object: %v (%s)", path, err, got.Body)
	}
	return doc
}

// raw issues one request against the harness origin and returns the whole
// outcome. It never decodes: the connector suite asserts on status codes and
// headers as often as on bodies, and a 404 has no JSON to decode.
func (e *env) raw(t *testing.T, method, path, payload string, headers map[string]string) httpResult {
	t.Helper()
	var body io.Reader
	if payload != "" {
		body = strings.NewReader(payload)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading response: %v", method, path, err)
	}
	return httpResult{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: string(raw)}
}

// TestMCPIsServedAtTheAPIOriginWithDiscovery proves the whole client
// bootstrap resolves on ONE origin: the 401 names an ABSOLUTE metadata URL,
// that URL is served here, and it points at this same issuer.
func TestMCPIsServedAtTheAPIOriginWithDiscovery(t *testing.T) {
	env := setupConnector(t)
	resp := env.listTools(t, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /mcp → %d, want 401", resp.StatusCode)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	metaURL := resourceMetadataParam(t, challenge)
	if !strings.HasPrefix(metaURL, env.origin) {
		t.Fatalf("resource_metadata = %q, want an absolute URL on %s", metaURL, env.origin)
	}
	doc := env.getJSON(t, strings.TrimPrefix(metaURL, env.origin))
	if doc["resource"] != env.origin+"/mcp" {
		t.Fatalf("resource = %v, want %s/mcp", doc["resource"], env.origin)
	}
	// The chain closes only if the authorization server it names is this
	// origin too: a client that has to cross origins to reach the token
	// endpoint is the split-origin failure the single mount exists to avoid.
	servers, ok := doc["authorization_servers"].([]any)
	if !ok || len(servers) != 1 || servers[0] != env.origin {
		t.Fatalf("authorization_servers = %v, want [%s]", doc["authorization_servers"], env.origin)
	}
	asDoc := env.getJSON(t, "/.well-known/oauth-authorization-server")
	if asDoc["issuer"] != env.origin || asDoc["token_endpoint"] != env.origin+"/oauth/token" {
		t.Fatalf("authorization-server metadata = %v, want issuer+token endpoint on %s", asDoc, env.origin)
	}
}

// TestMCPOnTheAPIServesTheAPIsOwnToolSurface proves the mount is composed over
// the registry the api ALREADY built rather than a second one: a passport
// minted through the session surface reaches the same governed tools the REST
// agent surface reports, so the two transports cannot drift in capability.
func TestMCPOnTheAPIServesTheAPIsOwnToolSurface(t *testing.T) {
	env := setupConnector(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := env.call(t, "POST", "/v1/passports", anyMap{
		"label": "connector client", "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}

	var rest agentToolListWire
	if status := env.call(t, "GET", "/v1/agent-tools", nil, nil, &rest); status != http.StatusOK {
		t.Fatalf("GET /v1/agent-tools → %d", status)
	}
	if len(rest.Data) == 0 {
		t.Fatal("the REST agent surface reports no tools, so there is nothing to compare the transport against")
	}

	got := env.listTools(t, minted.Token)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("authenticated POST /mcp → %d %s", got.StatusCode, got.Body)
	}
	for _, tool := range rest.Data {
		if !strings.Contains(got.Body, `"`+tool.Name+`"`) {
			t.Fatalf("tools/list omits %q, which /v1/agent-tools advertises: %s", tool.Name, got.Body)
		}
	}
}

// TestConnectorGateOffRemovesEveryConnectorRoute proves the deployment gate
// removes the connector as ONE group, and that the removal is
// indistinguishable route to route. Gating only the transport would leave
// unauthenticated client registration and a passport-minting token endpoint
// live with no off switch; answering the group with anything but the mux's
// own 404 would tell a prober which gate is closed.
func TestConnectorGateOffRemovesEveryConnectorRoute(t *testing.T) {
	e := setup(t) // the default deployment posture: the connector undeclared

	// Every route the gate mounts, and nothing may be left out: an endpoint
	// missing from this list is an endpoint nobody proved the gate covers.
	probes := []struct {
		method, path, payload string
	}{
		{http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{http.MethodGet, "/oauth/authorize?response_type=code&client_id=probe", ""},
		{http.MethodPost, "/oauth/register", `{"client_name":"probe","redirect_uris":["https://client.example/cb"]}`},
		{http.MethodPost, "/oauth/token", `grant_type=authorization_code`},
		{http.MethodPost, "/oauth/revoke", `token=mgp_probe`},
		{http.MethodGet, "/.well-known/oauth-authorization-server", ""},
		{http.MethodGet, "/.well-known/oauth-protected-resource", ""},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp", ""},
	}
	var want, wantFrom string
	for _, p := range probes {
		got := e.raw(t, p.method, p.path, p.payload, nil)
		if got.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s → %d, want 404: the gate must remove the route, not guard it",
				p.method, p.path, got.StatusCode)
		}
		fingerprint := observableFingerprint(got)
		if want == "" {
			want, wantFrom = fingerprint, p.method+" "+p.path
			continue
		}
		if fingerprint != want {
			t.Fatalf("%s %s answers %s but %s answers %s: the 404s must be indistinguishable",
				p.method, p.path, fingerprint, wantFrom, want)
		}
	}
}

// observableFingerprint is everything a prober can learn from one response
// apart from the clock: the status, the body, and EVERY header — not a chosen
// few. A gate that leaked which route exists would do it through whichever
// header a hand-picked list forgot (a lingering RFC 9728 pointer, a differing
// content type, an Allow), so the list is the whole set minus Date, which
// differs between any two requests for reasons that have nothing to do with
// routing.
func observableFingerprint(got httpResult) string {
	parts := make([]string, 0, len(got.Header)+2)
	parts = append(parts, fmt.Sprintf("status=%d", got.StatusCode), fmt.Sprintf("body=%q", got.Body))
	for name, values := range got.Header {
		if name == "Date" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", name, values))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// dereferences it, not later as an unexplained refusal.
func (e *connectorEnv) pathOn(t *testing.T, advertised string) string {
	t.Helper()
	if !strings.HasPrefix(advertised, e.origin+"/") {
		t.Fatalf("advertised URL %q is not on %s: the handshake would have to leave this origin", advertised, e.origin)
	}
	return strings.TrimPrefix(advertised, e.origin)
}

// rpc drives one JSON-RPC exchange on /mcp the way a connected client does and
// returns the result member. The negotiated revision is a parameter, not a
// constant: it travels on every request after initialize, so a server that
// answered a revision it then refuses would fail here.
//
//craft:ignore naked-any a JSON-RPC result is an open object by the protocol — asserting on one means reading it untyped
func (e *connectorEnv) rpc(t *testing.T, bearer, protocolVersion, payload string) map[string]any {
	t.Helper()
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + bearer}
	if protocolVersion != "" {
		headers["MCP-Protocol-Version"] = protocolVersion
	}
	got := e.raw(t, http.MethodPost, "/mcp", payload, headers)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp %s → %d %s", payload, got.StatusCode, got.Body)
	}
	return rpcResult(t, got.Body)
}

// rpcResult decodes one JSON-RPC response and fails on an error member or a
// body that does not decode WHOLE — a truncated response is exactly what the
// write-deadline test hunts for, so it must never read as a pass.
//
//craft:ignore naked-any a JSON-RPC result is an open object by the protocol — asserting on one means reading it untyped
func rpcResult(t *testing.T, body string) map[string]any {
	t.Helper()
	var envelope struct {
		JSONRPC string         `json:"jsonrpc"`
		Result  map[string]any `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("JSON-RPC response does not decode: %v (%s)", err, body)
	}
	if envelope.JSONRPC != "2.0" {
		t.Fatalf(`jsonrpc = %q, want "2.0": %s`, envelope.JSONRPC, body)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result == nil {
		t.Fatalf("JSON-RPC response carries neither a result nor an error: %s", body)
	}
	return envelope.Result
}

// toolText reads the text a successful tools/call returned. A refused tool
// answers 200 with isError set — the failure travels IN BAND — so a status
// check alone cannot tell a call that ran from one that was denied.
//
//craft:ignore naked-any a tools/call result is an open object by the protocol — asserting on one means reading it untyped
func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tools/call was refused: %v", result["content"])
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tools/call result carries no content: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("tools/call content[0] is not an object: %v", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("tools/call content[0] carries no text: %v", first)
	}
	return text
}

// stringField reads one required string member out of a discovery document.
// An absent or empty value is a document a client cannot act on, so it fails
// here rather than being carried forward as "".
//
//craft:ignore naked-any a discovery document is an open JSON object by RFC 8414/9728 — asserting on it means reading it untyped
func stringField(t *testing.T, doc map[string]any, key string) string {
	t.Helper()
	value, ok := doc[key].(string)
	if !ok || value == "" {
		t.Fatalf("discovery document carries no %s: %v", key, doc)
	}
	return value
}

// resourceMetadataParam pulls the resource_metadata URL out of an RFC 9728
// WWW-Authenticate challenge. A client cannot start discovery without it, so
// a challenge that omits it fails here rather than later as a mystery.
func resourceMetadataParam(t *testing.T, challenge string) string {
	t.Helper()
	const key = `resource_metadata="`
	start := strings.Index(challenge, key)
	if start < 0 {
		t.Fatalf("challenge %q carries no resource_metadata parameter", challenge)
	}
	rest := challenge[start+len(key):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("challenge %q has an unterminated resource_metadata value", challenge)
	}
	return rest[:end]
}
