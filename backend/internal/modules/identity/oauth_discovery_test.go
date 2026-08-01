// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The RFC 9728 protected-resource document must name the MCP URL itself,
// sourced from config, never the bare request origin — Anthropic's
// clients match "resource" against the MCP server URL exactly as the
// user enters it, including the path.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A grant type a client cannot see is a grant type it will not use: without
// refresh_token here, a connector asks for offline_access, stores the token
// it is handed, and never presents it — the connection dies at the access
// token's expiry with a live refresh credential in hand.
func TestServerMetadataAdvertisesBothGrantTypes(t *testing.T) {
	rec := httptest.NewRecorder()
	Handlers{}.OAuthServerMetadata(rec, httptest.NewRequest(http.MethodGet,
		"https://crm.example.com/.well-known/oauth-authorization-server", nil))

	var doc struct {
		GrantTypes []string `json:"grant_types_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"authorization_code", "refresh_token"} {
		if !slices.Contains(doc.GrantTypes, want) {
			t.Errorf("grant_types_supported = %v, want it to include %q", doc.GrantTypes, want)
		}
	}
}

// TestDiscoveryAdvertisesEveryGrantableScope is the fitness function behind
// oauthScopesSupported's claim to be derived: it reads the document a client
// actually fetches and holds it against the vocabulary the passport mint
// admits. A grantable scope missing from discovery is a scope no client asks
// for and therefore no human is ever offered — and the two lists drifting apart
// is exactly what a second hand-typed copy would allow.
func TestDiscoveryAdvertisesEveryGrantableScope(t *testing.T) {
	rec := httptest.NewRecorder()
	Handlers{}.OAuthServerMetadata(rec, httptest.NewRequest(http.MethodGet,
		"https://crm.example.com/.well-known/oauth-authorization-server", nil))

	var doc struct {
		Scopes []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for scope := range validScopes {
		if !slices.Contains(doc.Scopes, string(scope)) {
			t.Errorf("scopes_supported = %v, want it to include the grantable scope %q", doc.Scopes, scope)
		}
	}
	// And nothing beyond them but the session-lifetime marker: advertising a
	// scope the mint would refuse strands a client after the human consented.
	for _, advertised := range doc.Scopes {
		if advertised == scopeOfflineAccess {
			continue
		}
		if !validScopes[principal.Scope(advertised)] {
			t.Errorf("scopes_supported advertises %q, which the passport mint does not admit", advertised)
		}
	}
}

func TestProtectedResourceMetadataNamesTheMCPURLNotTheOrigin(t *testing.T) {
	h := Handlers{mcpResource: "https://crm.example.com/mcp"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"https://crm.example.com/.well-known/oauth-protected-resource", nil)
	// The forwarded-proto signal a terminating proxy supplies in production.
	// Stating it means the https assertions below rest on the header this
	// deployment actually relies on, not on whatever r.TLS happens to be.
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ProtectedResourceMetadata(rec, req)

	var doc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	// Anthropic: the resource field must match the MCP server URL exactly as
	// the user enters it, INCLUDING the path. The bare origin fails strict
	// clients.
	if doc.Resource != "https://crm.example.com/mcp" {
		t.Fatalf("resource = %q, want the canonical MCP URL", doc.Resource)
	}
	if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != "https://crm.example.com" {
		t.Fatalf("authorization_servers = %v, want the issuer origin first", doc.AuthorizationServers)
	}
}
