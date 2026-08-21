// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The agent tool surface, rebuilt from the server's current state.
//
// It lives beside server.go rather than in it because server.go is the
// INVENTORY — what this process serves — and this is one assembly step over
// that inventory. Splitting it also gives the inventory room to grow: the file
// had reached the length where a reader stops holding it at once, and every new
// handler set is one more line in it.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
)

// rebuildToolRegistry rebuilds the agent tool surface from the server's
// CURRENT state. Every option that changes what the registry composes over —
// the reply drafter, the send configuration — calls this rather than building
// its own registry, so applying two such options in either order lands on the
// same surface instead of the later one dropping the earlier one's wiring.
func (s *Server) rebuildToolRegistry(pool *pgxpool.Pool) {
	// The closure captures s and reads s.vault LAZILY at request time, so
	// rebuilding before WithKeyvault installs the vault is fine.
	// The gate and the registry take the SAME meter pointer: one refuses on the
	// bound, the other pays into it, and a surface where those were two
	// counters would step an agent up against a number nothing was charging.
	s.toolRegistry = registryWithGate(InstallationDB(pool),
		auth.NewGate(identity.NewService(pool), auth.WithQuota(s.quotaMeter)),
		s.replyDrafter, s.resolveOverlayIncumbent(pool), s.send, companyEnricher{srv: s},
		s.retrievalEmbedder, s.transcriptOnLanding, importsFor(s), s.log,
		agents.WithQuotaCharger(s.quotaMeter), agents.WithCostShare(s.quotaMeter))
}
