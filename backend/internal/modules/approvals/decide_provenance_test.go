// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The guard that keeps an agent-minted staging away from a server-side
// executor. The kind string alone cannot distinguish them — "enrich" names
// both a compose proposal flow and the tool behind three agent-reachable
// routes — so provenance does: a server-side proposal is staged by the system
// or by a human and carries no passport.
//
// The end-to-end path is exercised over a real database in
// compose/integration; this pins the predicate itself, which is where the
// distinction is actually made.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestServerProposedTurnsOnThePassport(t *testing.T) {
	minted := ids.From[ids.PassportKind](ids.NewV7())

	if serverProposed(row{Kind: "enrich", PassportID: &minted}) {
		t.Error("a staging carrying a passport was minted by an agent asserting one — " +
			"approving it must not invoke a server-side executor")
	}
	if !serverProposed(row{Kind: "enrich"}) {
		t.Error("a staging with no passport is a server-side proposal — its executor must still run, " +
			"or the provenance check would disable the confirm-first proposal flows it protects")
	}
}
