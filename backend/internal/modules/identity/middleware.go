// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The HTTP admission middleware fronting /v1: workspace resolution (slug →
// GUC) and session authentication (cookie → Principal), with the public-path
// and session-less connector-callback exemptions. Split out of handlers.go so
// each file stays one concept (and under the 500-LOC cap); the per-principal
// hand-offs (serveAsAgent/serveAsHuman) and helpers stay in handlers.go.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// publicPaths need no session; every other /v1 path 401s without one
// (the middleware only fronts the API — static assets never reach it).
var publicPaths = map[string]bool{
	"/v1/workspaces":  true,
	"/v1/auth/login":  true,
	"/v1/auth/logout": true,
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
	return strings.HasPrefix(path, "/v1/connectors/") && strings.HasSuffix(path, "/callback")
}

// Middleware chains workspace resolution and session authentication:
// slug → workspace GUC context; cookie → Principal. Public paths still
// get the workspace bound (login needs it), just no session requirement.
func (h Handlers) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if slug := workspaceSlug(r); slug != "" {
			wsID, err := h.svc.ResolveWorkspace(ctx, slug)
			if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
				httperr.Write(w, r, err)
				return
			}
			if err == nil {
				ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
			}
		}

		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// The anonymous booking surface has neither session nor workspace
		// header — its slug IS the tenant resolver. Everything else about
		// the request (workspace binding, principal, rate limits) is the
		// public-booking middleware's job, composed downstream.
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

		if _, ok := principal.WorkspaceID(ctx); !ok {
			httperr.Unauthorized(w, r, "unknown workspace")
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
