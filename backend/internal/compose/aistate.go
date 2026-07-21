// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "github.com/jackc/pgx/v5/pgxpool"

// AI state strings /readyz reports (ai-operational-spec §2): the runtime
// is one of these at any time, and — unlike every other ReadyCheck this
// package assembles — none of them can turn the probe unready. An
// AI-unconfigured deployment is a legitimate, ready deployment; the
// state is a visibility line, not a gate.
const (
	AIStateConfigured   = "configured"   // a declared ai-routing.yaml is bound
	AIStateFake         = "fake"         // --ai-fake, dev/test only
	AIStateUnconfigured = "unconfigured" // neither wired
)

// WithAIState sets the /readyz AI visibility line. cmd computes it from
// the same declared-routing/--ai-fake/neither switch that decides
// whether coldStartOptions/offerDraftOptions light up, so the probe
// never disagrees with what the process actually wired.
func WithAIState(state string) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.aiState = state }
}

// aiStateOrDefault is what /readyz reports: a role that never calls
// WithAIState (it wires no AI surfaces at all) still answers honestly
// rather than with an empty component.
func (s *Server) aiStateOrDefault() string {
	if s.aiState == "" {
		return AIStateUnconfigured
	}
	return s.aiState
}
