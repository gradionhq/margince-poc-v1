// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The egress backstop: the one gate under every write that leaves our boundary
// and lands in a third party's CRM. It lives beside the dispatch it guards
// (Dispatcher.updateInMode / archiveInMode call it) rather than inside
// dispatcher.go, so the routing concern and the authorization concern read as
// the separate things they are.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// refuseUngovernedAgentEgress is the backstop under EVERY write that lands in
// an external system of record. Such a write leaves our boundary and appears
// in a third party's CRM, and mirrored content is T2 — attacker-influenceable
// by design — so a manipulated agent must not place content there on its own
// authority (AC-OV-5).
//
// It answers the DECLARED unsupported-by-SoR result rather than "needs
// approval", because in this build there is no approval an agent could
// actually get for it: staging one requires an authority object a human can
// both see and release, and for a mirrored target neither holds — the
// decidability probe reads our own tables, and the redemption version pin
// re-reads a row that does not exist. Answering "needs human release" would
// name a path that dead-ends, which is exactly what the unsupported sentinel
// exists to avoid. Confirm-first agent write-back is therefore blocked on the
// approvals layer being able to describe a non-authoritative target at all —
// both its decidability probe and its redemption pin read our own tables. The
// released-approval check below is the seam such a change would plug into,
// which is why it stays even though nothing can set the marker on this path
// today: a future released approval must not be refused by the gate it was
// granted for.
//
// It gates only verbs the overlay adapter actually serves, so an agent asking
// for a permanently unsupported one still gets that answer from the provider
// (with its own object-RBAC and row-scope checks) rather than this one, and
// so "every write" stays true by construction as SupportsWrite changes.
//
// It sits on updateInMode/archiveInMode rather than Update/Archive so the
// REST write shadow, which dispatches through those directly, cannot slip
// underneath it. A human in their own seat is not gated here: that is object
// RBAC's question, per ADR-0055's split between seat authority and agent
// autonomy.
//
// Running ahead of the provider's own auth.Require means an agent with no
// grant learns the workspace runs on an incumbent before it learns it lacks
// permission. That discloses nothing: /me reports system_of_record.mode to
// every authenticated principal by contract, so the mode is not a secret this
// ordering could leak — and the answer is identical for a record that exists
// and one that does not, so it is no existence oracle either.
func refuseUngovernedAgentEgress(ctx context.Context, verb overlay.WriteVerb, et datasource.EntityType) error {
	if !overlay.SupportsWrite(verb, et) {
		return nil
	}
	if !isAgentPrincipal(ctx) {
		return nil
	}
	if agents.ApprovalRedeemed(ctx) {
		return nil
	}
	return fmt.Errorf(
		"an agent cannot write %s into this workspace's external system of record: %w",
		et, apperrors.ErrUnsupportedBySoR)
}

// isAgentPrincipal reports whether ctx acts under a passport rather than a
// human seat or a system role. AgentIdentity.Principal is the only production
// constructor of that type, so every agent surface — REST bearer, stdio and
// hosted MCP, the Surface-B runner — answers true here.
func isAgentPrincipal(ctx context.Context) bool {
	actor, ok := principal.Actor(ctx)
	return ok && actor.Type == principal.PrincipalAgent
}
