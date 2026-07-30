// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The RFC 8414 / RFC 9728 discovery documents a generic MCP client
// reads to find the A2 handshake.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// OAuthServerMetadata is the RFC 8414 discovery document. The issuer is
// the serving host — one issuer per workspace subdomain in production.
func OAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := requestIssuer(r)
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"registration_endpoint":                 issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"read", "draft", "write", "send", "enrich"},
	})
}

// ProtectedResourceMetadata is the RFC 9728 document: the resource
// names its authorization server so a generic MCP client can discover
// the handshake.
func ProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := requestIssuer(r)
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
	})
}

// requestIssuer reconstructs the externally visible origin, delegating to
// the one implementation in platform/httpserver so identity and compose
// share it rather than each carrying its own copy of the
// X-Forwarded-Proto handling.
func requestIssuer(r *http.Request) string { return httpserver.RequestOrigin(r) }
