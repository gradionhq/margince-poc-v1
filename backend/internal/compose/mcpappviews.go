// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The MCP App views' lifecycle, owned by the api's composition.
//
// It lives here rather than in modules/agents/apps because it is a COMPOSITION
// question in three ways at once: which process role reads the documents, which
// deployment gate decides whether to read them at all, and how the run loop is
// cancelled on shutdown. The module owns the fetch policy, the admission check
// and the snapshot; none of those knows which binary it is inside.
//
// THE API ROLE ONLY. The worker composes no view provider: two processes
// refreshing one snapshot would be two answers to a question that has one, and
// the worker serves no /mcp at all.

import (
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
)

// WithMCPAppViews hands this Server the view provider its process role built.
//
// The provider arrives ALREADY PRIMED, which is why this takes one rather than
// an origin: the bounded startup fetch needs a context and a cancellable refresh
// loop, and neither is a thing an Option can carry. The api's boot owns that
// lifecycle, exactly as it owns the inline relay's — see cmd/api/mcpapps.go.
//
// Absent, the Server composes no view provider, publishes no `ui://` document
// and emits no `_meta.ui` — and every tool that would have carried one still
// answers in text, which is the exit criterion this whole capability is held to.
func WithMCPAppViews(views *apps.Provider) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.appViews = views
	}
}

// heldView answers the tool surface's question: is this `ui://` document one we
// are serving right now?
//
// Nil for a role with no view provider, which is what makes `_meta.ui` absent
// rather than dangling — see agents.WithHeldViews.
func (s *Server) heldView() func(string) bool {
	if s.appViews == nil {
		return nil
	}
	return s.appViews.Holds
}

// writeMCPAppMetrics renders the view-availability section, or nothing at all
// for a role that serves no views — the same "declared or absent" posture every
// other section of /metrics takes.
func (s *Server) writeMCPAppMetrics(w io.Writer) {
	if s.appViews != nil {
		s.appViews.WriteMetrics(w)
	}
}
