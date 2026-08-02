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

// scopesAdvertisedBy renders one discovery document and returns the
// scopes_supported a client would read off it. Both documents are asserted
// against the ONE closed vocabulary through this, so neither claim rests on a
// second hand-typed copy of the five verbs.
func scopesAdvertisedBy(t *testing.T, document http.HandlerFunc, url string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	document(rec, httptest.NewRequest(http.MethodGet, url, nil))
	var doc struct {
		Scopes []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Scopes
}

// TestDiscoveryAdvertisesEveryGrantableScope is the fitness function behind the
// derived-vocabulary claim: it reads the documents a client actually fetches and
// holds each against the vocabulary the passport mint admits. A grantable scope
// missing from discovery is a scope no client asks for and therefore no human is
// ever offered; a scope present that the mint would refuse strands a client
// after the human consented. Both documents answer to the same list, so adding
// a verb to the vocabulary cannot leave either one behind.
func TestDiscoveryAdvertisesEveryGrantableScope(t *testing.T) {
	h := Handlers{mcpResource: "https://crm.example.com/mcp"}
	for _, document := range []struct {
		name   string
		scopes []string
	}{
		{"authorization server", scopesAdvertisedBy(t, h.OAuthServerMetadata,
			"https://crm.example.com/.well-known/oauth-authorization-server")},
		{"protected resource", scopesAdvertisedBy(t, h.ProtectedResourceMetadata,
			"https://crm.example.com/.well-known/oauth-protected-resource")},
	} {
		for scope := range validScopes {
			if !slices.Contains(document.scopes, string(scope)) {
				t.Errorf("%s scopes_supported = %v, want it to include the grantable scope %q",
					document.name, document.scopes, scope)
			}
		}
		for _, advertised := range document.scopes {
			if advertised == scopeOfflineAccess {
				continue
			}
			if !validScopes[principal.Scope(advertised)] {
				t.Errorf("%s scopes_supported advertises %q, which the passport mint does not admit",
					document.name, advertised)
			}
		}
	}
}

// TestOnlyTheAuthorizationServerAdvertisesOfflineAccess pins the asymmetry
// between the two documents. offline_access buys token lifetime, not access to
// any record, so it belongs to the authorization server that issues the
// refresh token and never to the resource whose records it does not reach —
// and it is not a passport scope, so a resource advertising it invites a
// request the mint would refuse.
func TestOnlyTheAuthorizationServerAdvertisesOfflineAccess(t *testing.T) {
	h := Handlers{mcpResource: "https://crm.example.com/mcp"}

	server := scopesAdvertisedBy(t, h.OAuthServerMetadata,
		"https://crm.example.com/.well-known/oauth-authorization-server")
	if !slices.Contains(server, scopeOfflineAccess) {
		t.Errorf("authorization server scopes_supported = %v, want %q so a client knows it may renew",
			server, scopeOfflineAccess)
	}
	if len(server) != len(passportScopeVocabulary)+1 {
		t.Errorf("authorization server scopes_supported = %v, want the closed vocabulary plus %q and nothing else",
			server, scopeOfflineAccess)
	}

	resource := scopesAdvertisedBy(t, h.ProtectedResourceMetadata,
		"https://crm.example.com/.well-known/oauth-protected-resource")
	if slices.Contains(resource, scopeOfflineAccess) {
		t.Errorf("protected resource scopes_supported = %v, want no %q: it grants no access to a record",
			resource, scopeOfflineAccess)
	}
	if len(resource) != len(passportScopeVocabulary) {
		t.Errorf("protected resource scopes_supported = %v, want exactly the closed vocabulary", resource)
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
