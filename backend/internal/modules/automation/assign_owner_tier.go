// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The dynamic assign_owner tier (AUTO-T07, catalog_actions.go's
// tierDynamic): "reassign-at-scale is the held (yellow) form of
// assign/reassign" — the same ADR-0026 §3 shape advance_deal already
// runs (🟢 between open stages, 🟡 to Won/Lost). Scale is a property of
// the automation's OWN filter/scope, never the caller — an agent or a
// human retrying the same call cannot upgrade or downgrade the tier by
// how they ask.

import "github.com/gradionhq/margince/backend/internal/shared/ports/mcp"

// AssignOwnerScope is the fire-time scale signal the resolver reads.
// Bulk true means the automation's own scope widened this firing past a
// single entity — e.g. a bulk reassignment run over a saved filter,
// rather than one automation instance reacting to one triggering event.
//
// No shipped automation sets Bulk today: every Action this codebase
// plans carries exactly one datasource.EntityRef in its Target (a
// single entity by construction — see ApplyActions' switch), and
// route_lead — the only live user of "assign_owner" — resolves its
// owner through people's own lead-routing SQL, never through this
// resolver or ApplyActions' ActionAssignOwner case at all. Bulk exists
// so the day a workspace author defines a genuinely bulk-scope
// reassignment automation, ApplyActions already has a resolver ready to
// hold it at 🟡 — wiring that automation's Plan to set Bulk is future
// work, not something to fabricate here.
type AssignOwnerScope struct {
	Bulk bool
}

// resolveAssignOwnerTier is the pure, unit-tested resolution point:
// single-entity (Bulk == false) is 🟢 auto-apply, at-scale (Bulk == true)
// is 🟡 stage-for-approval. A resolver may only ever raise never lower
// (mcp.TierResolver's invariant) — this one has exactly the two inputs
// AssignOwnerScope can carry, so there is no third case to get wrong.
func resolveAssignOwnerTier(scope AssignOwnerScope) mcp.RiskTier {
	if scope.Bulk {
		return mcp.TierYellow
	}
	return mcp.TierGreen
}
