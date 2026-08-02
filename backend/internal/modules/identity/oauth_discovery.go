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
// the serving host — one issuer per workspace subdomain in production. A
// method on Handlers (not a package func) because the sibling protected-
// resource document needs the handlers' injected config.
func (h Handlers) OAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := requestIssuer(r)
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + authorizePath,
		"token_endpoint":         issuer + "/oauth/token",
		"registration_endpoint":  issuer + "/oauth/register",
		// RFC 7009: a client that cannot see this here will never call it —
		// it hands back a credential and ends the connection on its own
		// initiative, not on a server-side hint.
		"revocation_endpoint":      issuer + "/oauth/revoke",
		"response_types_supported": []string{oauthResponseTypeCode},
		// refresh_token is advertised because a client that cannot see it
		// here will not present one: it asks for offline_access, stores the
		// token it gets, and never renews with it.
		"grant_types_supported":                 []string{"authorization_code", oauthRefreshToken},
		"code_challenge_methods_supported":      []string{pkceMethodS256},
		"token_endpoint_auth_methods_supported": []string{"none"},
		// offline_access is listed so Claude appends it when it wants a
		// refresh token (§5.2) — it is a session-lifetime marker, never a
		// passport scope, so the exchange records it as the grant's
		// refresh_allowed and strips it from the scopes (oauth_token.go).
		"scopes_supported": oauthScopesSupported,
	})
}

// ProtectedResourceMetadata is the RFC 9728 document a generic MCP client
// reads to find the authorization server for a given resource. The
// resource field is the canonical MCP URL itself (h.mcpResource),
// injected at boot from --public-base-url — Anthropic's clients require
// it to match the MCP server URL exactly as the user enters it,
// including the path, so it can never be the bare request origin.
func (h Handlers) ProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		oauthParamResource:         h.mcpResource,
		"authorization_servers":    []string{requestIssuer(r)},
		"bearer_methods_supported": []string{"header"},
		// The vocabulary belongs HERE, not only in the authorization server's
		// document: this is the RFC 9728 §2 field a client reads to learn what
		// it may ask for at this resource, and a scope absent from it is one no
		// client will ever name. What it does not do is bound a connection —
		// the grant is the passport the human lent, so a client that names
		// nothing still receives everything in it.
		// offline_access is absent on purpose: it buys token lifetime, not
		// access to this resource, so it is the authorization server's to
		// advertise and never a passport scope.
		"scopes_supported": resourceScopesSupported,
	})
}

// requestIssuer reconstructs the externally visible origin, delegating to
// the one implementation in platform/httpserver so identity and compose
// share it rather than each carrying its own copy of the
// X-Forwarded-Proto handling.
func requestIssuer(r *http.Request) string { return httpserver.RequestOrigin(r) }
