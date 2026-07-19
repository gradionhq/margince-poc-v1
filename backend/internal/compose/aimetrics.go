// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithAIMetrics sets this role's /metrics AI renderer. A process resolves
// one ModelPath and registers this exactly once over it, so /metrics
// exposes one AI metric family instance for the process's one Router.
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
