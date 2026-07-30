// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The A2 hosted transport's edge on the api origin: the per-request
// authenticate closure the MCP handler runs, and the deployment gate that
// decides whether the route exists at all. The tool surface itself is
// srv.toolRegistry — the SAME registry the REST agent surface composes, so
// the two transports cannot differ in capability.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The MCP server identity reported in the initialize handshake — the same
// pair the stdio transport reports, because it is the same tool surface.
const (
	mcpServerName    = "margince-crm"
	mcpServerVersion = "0.1.0"
)

// errMissingBearer is the authenticate refusal for a request that carries no
// usable credential. It never reaches the client verbatim: the transport
// answers 401 + the RFC 9728 pointer, which is what a client acts on.
var errMissingBearer = errors.New("missing bearer token")

// mcpHandler builds the /mcp transport over the registry this Server already
// composed. It returns nil when the deployment gate is off — the caller then
// mounts no route, so turning the connector off removes the surface rather
// than guarding it.
func (s *Server) mcpHandler(pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	if !s.mcpConnectorEnabled {
		return nil
	}
	// An operator turning the gate on needs one line confirming the surface
	// came up, and how much of it: a mount that silently serves nothing is
	// indistinguishable from a mount that never happened.
	log.Info("mcp: hosted connector transport mounted", "path", "/mcp", "tools", len(s.toolRegistry.Specs()))
	return agents.NewHTTPHandler(s.toolRegistry, mcpAuthenticate(identity.NewService(pool)),
		agents.ResourceMetadataChallenge, mcpServerName, mcpServerVersion)
}

// mcpAuthenticate binds one request to its agent principal. It runs on EVERY
// exchange: the passport, the granting human's seat and their RBAC are all
// re-derived, so a revocation or demotion takes effect on the next call
// rather than after a reconnect.
func mcpAuthenticate(auth *identity.Service) func(*http.Request) (context.Context, error) {
	return func(r *http.Request) (context.Context, error) {
		wsID, err := auth.InstallationWorkspace(r.Context())
		if err != nil {
			return nil, err
		}
		ctx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		// bearerToken requires the scheme name and a non-empty credential
		// after it. A TrimPrefix-style read would accept a header that never
		// carried the prefix, turning an unrelated credential (or a Basic
		// header) into a passport lookup.
		bearer := bearerToken(r.Header.Get("Authorization"))
		if bearer == "" {
			return nil, errMissingBearer
		}
		agent, err := auth.AuthenticateAgent(ctx, bearer)
		if err != nil {
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}
}
