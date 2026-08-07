// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The whole client handshake, walked once, end to end: from a refusal that
// names where the authorization server is, through dynamic registration and
// consent, to a tool call that changes a record — and never once off this
// origin. The per-step suites around it each prove one link; only this one
// proves the chain closes.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// TestAConnectorCompletesTheWholeHandshakeOnOneOrigin is the phase's headline
// claim, and the one thing every per-step test around it cannot say: a client
// that starts knowing ONLY the MCP URL reaches a working tool call without
// ever leaving this origin. Each endpoint it uses is one the server itself
// advertised, and pathOn asserts every advertised URL against the origin it
// is dereferenced from — so a chain that quietly crossed origins fails here
// rather than inside a client nobody can debug.
func TestAConnectorCompletesTheWholeHandshakeOnOneOrigin(t *testing.T) {
	e := setupConnector(t)
	advertised := e.discover(t)

	// Dynamic client registration: no operator ever provisioned this client.
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if status := e.Call(t, "POST", advertised.register, apptest.AnyMap{
		"client_name": "one-origin connector", "redirect_uris": []string{oauthRedirect},
	}, nil, &registered); status != http.StatusCreated || registered.ClientID == "" {
		t.Fatalf("DCR at %s → %d %+v", advertised.register, status, registered)
	}
	o := &oauthEnv{
		connectorEnv: e, clientID: registered.ClientID,
		verifier: strings.Repeat("one-origin-verifier-", 3),
	} // 60 chars, RFC 7636 range

	// Consent, then the exchange, both bound to the audience the resource
	// document named rather than one this test invented.
	code := o.authorize(t, url.Values{"resource": {advertised.resource}})
	status, exchanged := o.exchange(t, url.Values{"code": {code}, "resource": {advertised.resource}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, exchanged)
	}
	token, _ := exchanged["access_token"].(string)
	if token == "" {
		t.Fatalf("the exchange returned no access token: %v", exchanged)
	}

	// initialize settles the protocol revision and mints the session id every
	// later request on this connection carries.
	const requested = "2025-06-18"
	initialized := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"`+requested+
			`","clientInfo":{"name":"conformance","version":"1"}}}`,
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + token})
	if initialized.StatusCode != http.StatusOK {
		t.Fatalf("initialize → %d %s", initialized.StatusCode, initialized.Body)
	}
	negotiated, _ := rpcResult(t, initialized.Body)["protocolVersion"].(string)
	if negotiated != requested {
		t.Fatalf("protocolVersion = %q, want the requested %q back", negotiated, requested)
	}
	if initialized.Header.Get("Mcp-Session-Id") == "" {
		t.Fatal("initialize minted no Mcp-Session-Id, so the client holds no session to present or tear down")
	}

	// What the client can see, then what it can do — both on the revision
	// just negotiated, which every request from here on declares.
	list := e.rpc(t, token, negotiated, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools, ok := list["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list answered no tools, so there is nothing for the client to call: %v", list)
	}
	const created = "One Origin Person"
	call := e.rpc(t, token, negotiated,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"create_record",`+
			`"arguments":{"record_type":"person","fields":{"full_name":"`+created+`"}}}}`)
	if text := toolText(t, call); !strings.Contains(text, created) {
		t.Fatalf("tools/call answered %q, which does not carry the record it was asked to create", text)
	}
	// The handshake ends in a real effect or it ends in nothing: a surface
	// answering from a stub would satisfy every assertion above.
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM person WHERE full_name = $1`, created)
}

// advertisedEndpoints is everything a client learns before it holds any
// credential: where to register, where to send the human, where to redeem the
// code, and the audience all three are bound to. The endpoints are PATHS
// because pathOn has already asserted the origin off each absolute URL — the
// one place this suite makes the same-origin claim.
type advertisedEndpoints struct {
	register  string
	authorize string
	token     string
	resource  string
}

// discover walks the RFC 9728 → RFC 8414 chain the way a client does: from the
// refusal, to the protected-resource document it points at, to the
// authorization server that document names. Every URL it follows is one the
// previous document handed it, so a chain that crossed origins — a separate
// authorization server, a document naming someone else's issuer — fails here
// instead of much later as an unexplained client error.
func (e *connectorEnv) discover(t *testing.T) advertisedEndpoints {
	t.Helper()
	unauth := listTools(e.AppEnv, t, "")
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /mcp → %d, want 401", unauth.StatusCode)
	}
	resourceDoc := getJSON(e.AppEnv, t, e.pathOn(t, resourceMetadataParam(t, unauth.Header.Get("WWW-Authenticate"))))
	servers, ok := resourceDoc["authorization_servers"].([]any)
	if !ok || len(servers) != 1 || servers[0] != e.origin {
		t.Fatalf("authorization_servers = %v, want [%s]", resourceDoc["authorization_servers"], e.origin)
	}
	asDoc := getJSON(e.AppEnv, t, "/.well-known/oauth-authorization-server")
	out := advertisedEndpoints{
		register:  e.pathOn(t, stringField(t, asDoc, "registration_endpoint")),
		authorize: e.pathOn(t, stringField(t, asDoc, "authorization_endpoint")),
		token:     e.pathOn(t, stringField(t, asDoc, "token_endpoint")),
		resource:  stringField(t, resourceDoc, "resource"),
	}
	// The consent and exchange helpers drive fixed paths, so the advertised
	// values are pinned to them: without this the document could name
	// endpoints the handshake never actually visits and still pass.
	if out.authorize != "/oauth/authorize" || out.token != "/oauth/token" {
		t.Fatalf("advertised authorize/token = %q/%q, not the endpoints this handshake drives",
			out.authorize, out.token)
	}
	return out
}
