// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// The quantitative third of "scope ∧ tier ∧ quota" (interfaces.md §2), and the
// §2.4 ladder's refusing half.
//
// The first two terms are BOOLEAN — may this caller run this verb at all — and
// they are the only ones that catch an agent doing something it was never
// allowed to do. This one is the only one that catches an agent doing something
// it IS allowed to do, at a volume nobody intended (api-rate-limits §2.5). It is
// why an in-scope, correctly-tiered, read-only Passport reading the whole
// workspace is a gated event rather than a Tuesday.
//
// This half only ever REFUSES. Nothing here charges a counter: the surface pays
// where records and effects leave it (modules/agents), and a gate that could
// both admit and charge could admit itself.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Quota answers what the calling agent has spent this window on one counter. It
// is an interface rather than the concrete meter so this package — the one
// admission point — stays testable without a Redis client, and so a deployment
// that has composed no bound is a visible nil rather than a meter that silently
// answers "plenty".
type Quota interface {
	Read(ctx context.Context, c agentquota.Counter) agentquota.Reading
}

// QuotaExceededError is a volume refusal, carrying the reading it was made from.
//
// It is a TYPE rather than a message because the two halves of the ladder are
// told apart by it: a refusal on a releasable counter has somewhere to go — the
// surface stages the question for the human who lent the Passport — and one on a
// hard stop does not. A caller matching on prose would eventually stage a
// release for a quota nothing can release.
//
// It unwraps to the sentinel interfaces.md §0 reserves for MCP-SESS-*, so every
// existing errors.Is check and every transport that maps sentinels to wire codes
// keeps working without learning this type.
type QuotaExceededError struct {
	// Tool is the call that was refused. Empty on the REST read path, which has
	// no tool spec to name.
	Tool string
	// Reading is the window as the meter read it: the counter, what was
	// observed, the effective limit, and the window a release must name.
	Reading agentquota.Reading
}

// Releasable reports whether a human can answer this refusal — the difference
// between BYO-STEP-1/2 (step-up and batch-confirm) and BYO-STEP-3/4 (hard stop
// and suspension).
func (e *QuotaExceededError) Releasable() bool { return e.Reading.Counter.Releasable() }

// Unwrap makes every existing budget check see this as what it is.
func (e *QuotaExceededError) Unwrap() error { return apperrors.ErrBudgetExceeded }

// Error states the numbers and what ends the refusal, because those are two
// different things per rung and an agent's next move depends on which it got.
// A releasable refusal names the human; a hard stop names the window, so a
// client does not sit waiting for an approval nobody can grant.
func (e *QuotaExceededError) Error() string {
	what := "this agent"
	if e.Tool != "" {
		what = e.Tool
	}
	if e.Releasable() {
		return fmt.Sprintf(
			"%s: this agent has spent %d of its %d %s for this window; the person who connected it must confirm before it continues",
			what, e.Reading.Observed, e.Reading.Limit, e.Reading.Counter)
	}
	return fmt.Sprintf(
		"%s: this agent has spent %d of its %d %s for this window, and that limit holds until the window rolls; no approval lifts it",
		what, e.Reading.Observed, e.Reading.Limit, e.Reading.Counter)
}

// refuseOnQuota applies every volume bound one tool call is subject to, and
// answers the first one it crosses.
//
// CALLS IS ASKED FIRST, and the order is a decision. It is the ceiling every
// other quota sits under (BYO-STEP-4's suspension), so a Passport that has
// crossed it is refused for every verb — and answering the per-kind quota
// instead would tell a suspended caller which of its allowances still has
// headroom, which is a map of what to spend next.
//
// Then the counter the call itself belongs to, DERIVED from the spec rather
// than listed, by the same function the charge point uses (agentquota.CounterFor).
// One derivation means the quota that refuses and the quota that is paid can
// never be two different quotas.
func (g *Gate) refuseOnQuota(ctx context.Context, spec mcp.ToolSpec) error {
	if g.quota == nil {
		return nil
	}
	for _, c := range []agentquota.Counter{agentquota.Calls, agentquota.CounterFor(spec)} {
		reading := g.quota.Read(ctx, c)
		if reading.Exceeded {
			return &QuotaExceededError{Tool: spec.Name, Reading: reading}
		}
	}
	return nil
}
