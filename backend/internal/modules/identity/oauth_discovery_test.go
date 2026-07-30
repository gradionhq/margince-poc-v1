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

func TestProtectedResourceMetadataNamesTheMCPURLNotTheOrigin(t *testing.T) {
	h := Handlers{mcpResource: "https://crm.example.com/mcp"}
	rec := httptest.NewRecorder()
	h.ProtectedResourceMetadata(rec, httptest.NewRequest(http.MethodGet,
		"https://crm.example.com/.well-known/oauth-protected-resource", nil))

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
