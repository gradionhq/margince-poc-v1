// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The RFC 8414 / RFC 9728 discovery documents a generic MCP client
// reads to find the A2 handshake.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
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

// requestIssuer reconstructs the externally visible origin. TLS
// terminates ahead of the chassis in production, so the forwarded
// proto wins when present.
func requestIssuer(r *http.Request) string {
	// Only the two legitimate values are honored; anything else in the
	// forwarded header is attacker noise. Host itself must be sanitized
	// by the fronting proxy — the metadata documents say so.
	scheme := "https"
	switch r.Header.Get("X-Forwarded-Proto") {
	case "https":
	case "http":
		scheme = "http"
	default:
		if r.TLS == nil {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}
