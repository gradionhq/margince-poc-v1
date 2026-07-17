// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The HTTP admission middleware fronting /v1: singleton-organization
// binding (installation → GUC, A107/ADR-0061) and session authentication
// (cookie → Principal), with the public-path and session-less
// connector-callback exemptions. Split out of handlers.go so each file
// stays one concept (and under the 500-LOC cap); the per-principal
// hand-offs (serveAsAgent/serveAsHuman) and helpers stay in handlers.go.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// publicPaths need no session; every other /v1 path 401s without one
// (the middleware only fronts the API — static assets never reach it).
var publicPaths = map[string]bool{
	"/v1/auth/capabilities":    true,
	"/v1/auth/login":           true,
	"/v1/auth/logout":          true,
	"/v1/auth/forgot-password": true,
	"/v1/auth/reset-password":  true,
	// The OAuth AS endpoints authenticate by their own means: DCR is
	// open (public clients + PKCE), token exchange proves possession via
	// the code + verifier. authorize is NOT here — it demands a session.
	"/oauth/register": true,
	"/oauth/token":    true,
}

// isConnectorOAuthCallback matches the capture-connector OAuth redirect
// targets (/v1/connectors/{provider}/callback). They are session-less by
// construction; the connectorOAuthCallback handler authenticates via the
// signed `state` parameter, never a cookie.
func isConnectorOAuthCallback(path string) bool {
	// Match EXACTLY /v1/connectors/{provider}/callback — a single provider
	// segment. A prefix/suffix test alone would also admit deeper paths like
	// /v1/connectors/gmail/admin/callback, widening the session bypass.
	rest, ok := strings.CutPrefix(path, "/v1/connectors/")
	if !ok {
		return false
	}
	provider, ok := strings.CutSuffix(rest, "/callback")
	return ok && provider != "" && !strings.Contains(provider, "/")
}

// Middleware chains organization binding and session authentication: the
// installation's singleton workspace → GUC context; cookie → Principal.
// One installation serves one organization (A107/ADR-0061), so no request
// selects a tenant — the server resolves it. Public paths still get the
// workspace bound (login needs it), just no session requirement.
func (h Handlers) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		wsID, err := h.svc.InstallationWorkspace(ctx)
		switch {
		case errors.Is(err, ErrNotBootstrapped), errors.Is(err, ErrMultipleWorkspaces):
			// An availability state, not an authentication one: the
			// installation cannot serve until an operator bootstraps it
			// (or resolves the invariant violation). Named plainly — the
			// condition is operator-facing and discloses no tenant data.
			httperr.ServiceUnavailable(w, r, "installation not ready: "+err.Error())
			return
		case err != nil:
			httperr.Write(w, r, err)
			return
		}
		ctx = principal.WithWorkspaceID(ctx, wsID.UUID)

		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// The anonymous booking surface needs no session; the singleton
		// organization is already bound above. Everything else about the
		// request (principal, rate limits) is the public-booking
		// middleware's job, composed downstream.
		if strings.HasPrefix(r.URL.Path, "/v1/public/") {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// A capture-connector OAuth callback (provider → CRM redirect) arrives
		// with neither a session cookie (SameSite blocks it on the cross-site
		// redirect) nor a workspace slug. Its signed `state` is the auth: the
		// handler verifies it and rebuilds the workspace + granting human from
		// it before persisting. So it passes the session/workspace gate here.
		if isConnectorOAuthCallback(r.URL.Path) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Agents present a passport bearer token. Agent authority is
		// governed identically on every transport (ADR-0055): reads need
		// the read scope here; a MUTATING call is not refused at the
		// transport — it proceeds into the contract router, where the
		// agent gate resolves the operation's 🟢/🟡 tier against the
		// tool's declared scope and either admits, stages an approval,
		// or default-denies an un-tiered operation.
		if bearer := bearerToken(r); bearer != "" {
			h.serveAsAgent(ctx, w, r, next, bearer)
			return
		}
		h.serveAsHuman(ctx, w, r, next)
	})
}
