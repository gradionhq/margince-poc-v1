// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithAIMetrics sets this role's /metrics AI renderer. coldStartOptions
// and offerDraftOptions each call this once for their own ModelPath, but
// every path's Router shares the one process-wide counter collector
// (ai/metrics.go), so each registration renders the identical total —
// last-wins is correct here, and it keeps the exposition to one AI
// metric family instance instead of a duplicate per registered surface.
func WithAIMetrics(write func(io.Writer)) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.aiMetrics = write
	}
}

// writeAIMetrics renders this role's AI counters exactly once. A role
// with none wired writes nothing — /metrics stays honest about an
// AI-less process rather than emitting an empty or fabricated counter
// family.
func (s Server) writeAIMetrics(w io.Writer) {
	if s.aiMetrics != nil {
		s.aiMetrics(w)
	}
}
