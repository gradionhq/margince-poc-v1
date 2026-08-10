// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What this surface OWES the per-Passport volume counters (MCP-SESS-*,
// api-rate-limits-and-abuse §2.2).
//
// The bound has two halves and they live apart on purpose. platform/auth decides
// admission on them — that is the ONE place a governed action is admitted, and a
// quota is an admission term ("scope ∧ tier ∧ quota"). This file is the other
// half: what an answer COSTS, charged where records and effects leave the
// surface. Splitting them is what keeps a registry from being able to admit
// itself.
//
// ONE RULE DECIDES WHAT A FAILED CHARGE DOES: refuse only while nothing has
// happened yet. Everything below follows from it.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// QuotaCharger records what a call spent against the caller's counters.
// Registry only ever charges; reading a counter and refusing on it belongs to
// the one admission point, so this half of the seam cannot be used to decide
// anything.
type QuotaCharger interface {
	Consume(ctx context.Context, c agentquota.Counter, n int) error
}

// RegistryOption configures a Registry at construction.
type RegistryOption func(*Registry)

// WithQuotaCharger installs the meter this surface's calls are charged against.
// It is the same pointer compose hands the admission gate as its Quota, so the
// half that refuses and the half that pays can never end up counting against
// different windows. A registry without one records nothing — the composition
// no agent principal reaches (see auth.WithQuota).
func WithQuotaCharger(quota QuotaCharger) RegistryOption {
	return func(r *Registry) { r.quota = quota }
}

// chargeCall records one admitted tool call against MCP-SESS-CALLS.
//
// It runs after admission and BEFORE the handler, which is the only point at
// which the answer to "has anything happened yet?" is no. That is what lets it
// refuse: a call this surface cannot count is a call it does not make, and the
// ceiling MCP-SESS-CALLS defends is the one every other quota sits under —
// leaving it uncounted while Redis blinks would let a flood through the gap.
//
// It charges ADMITTED calls, not attempted ones. A call the gate turned away
// never ran, and counting it would let a caller exhaust its own ceiling with
// requests it was never allowed to make — which an attacker holding a
// low-scoped passport could do to any workspace's real agent.
// refusable says whether this charge may still refuse the call. It is false
// once a human's approval has been consumed: the redemption committed its own
// transaction, so refusing here would burn that approval on a call that never
// ran and can never be redeemed again.
type refusable bool

const (
	nothingHasHappenedYet refusable = true
	anApprovalWasConsumed refusable = false
)

func (r *Registry) chargeCall(ctx context.Context, spec mcp.ToolSpec, mayRefuse refusable) error {
	if r.quota == nil {
		return nil
	}
	err := r.quota.Consume(ctx, agentquota.Calls, 1)
	if err == nil {
		return nil
	}
	slog.ErrorContext(ctx, "recording an admitted tool call against the call ceiling failed",
		"tool", spec.Name, "refusable", bool(mayRefuse), "err", err)
	if !mayRefuse {
		return nil
	}
	return fmt.Errorf(
		"crmagents: %s could not be counted against this agent's call ceiling, so it was not run: %w",
		spec.Name, apperrors.ErrBudgetExceeded)
}

// chargeAnswer records what one successful answer cost, at the one place every
// tool's result passes through. Charging per TOOL instead would be a list to
// maintain, and the tool added next is the one that forgets — which is exactly
// how a densely-joined answer becomes the cheapest bulk read on the surface
// (A139).
//
// TWO CHARGES, because an answer can be two things at once. Every record that
// leaves counts toward MCP-SESS-READS whatever tool produced it — a write that
// returns the row it changed handed over a record — and the ACT counts toward
// the counter its own kind names, derived by the same function the gate refuses
// on so the two can never mean different things. A read-only tool's act IS its
// records, so it is charged once.
//
// It charges only after a SUCCESSFUL answer: these counters measure what the
// agent was actually given, and a handler that failed gave it none.
func (r *Registry) chargeAnswer(ctx context.Context, spec mcp.ToolSpec, served int) error {
	if r.quota == nil {
		return nil
	}
	if err := r.charge(ctx, spec, agentquota.Reads, served); err != nil {
		return err
	}
	if act := agentquota.CounterFor(spec); act != agentquota.Reads {
		return r.charge(ctx, spec, act, 1)
	}
	return nil
}

// charge records n against one counter and decides what an unrecordable charge
// costs the caller.
//
// A charge that cannot be recorded REFUSES a READ, and only a read. If the
// surface cannot count what it is about to hand over, it does not hand it over
// — the gate's rule on the way in, applied on the way out.
//
// A WRITE or a SEND is served anyway, and the asymmetry is the whole point. By
// the time this runs the mutation has committed and any approval it redeemed is
// consumed: `send_email` has SENT. Withholding that result would report a
// completed, irreversible act as a failure, and the caller — reasonably —
// retries it. An uncounted write is a small accounting loss; a second email is
// not. So the act is logged and returned, and the counter absorbs the undercount.
//
// Logging and serving a READ was tried and is wrong. It looks contained, because
// the gate fails closed while the counter is unreachable — but a charge lost to
// a TRANSIENT write error is lost for good: Redis recovers, the counter comes
// back short, and those records are read again for free. Every blip would
// quietly raise the ceiling.
func (r *Registry) charge(ctx context.Context, spec mcp.ToolSpec, c agentquota.Counter, n int) error {
	if n <= 0 {
		return nil
	}
	err := r.quota.Consume(ctx, c, n)
	if err == nil {
		return nil
	}
	slog.ErrorContext(ctx, "recording what an answer spent against its quota failed",
		"tool", spec.Name, "quota", string(c), "amount", n, "read_only", spec.ReadOnly(), "err", err)
	if !spec.ReadOnly() || spec.Egress {
		// The effect already happened. Reporting it as a failure is worse than
		// an uncounted write.
		//
		// Egress is named separately rather than assumed to imply a write:
		// CounterFor admits a READ-scoped egress tool, and for one of those the
		// send has already left the workspace by the time this runs. Withholding
		// its answer would invite exactly the retry this branch exists to
		// prevent, on the one call where a retry costs the most.
		return nil
	}
	return fmt.Errorf(
		"crmagents: %s spent %d %s that could not be counted against this agent's quota, so the answer is withheld: %w",
		spec.Name, n, c, apperrors.ErrBudgetExceeded)
}

// CostShareReader answers what this agent has spent of its share of the
// workspace AI budget (MCP-SESS-COST).
//
// It is a one-method seam rather than the meter's general reader on purpose: a
// registry that could read the counters it pays into could decide whether to
// pay them, which is the split between this file and platform/auth. Cost is the
// one counter safe to read here because it refuses nothing — the only thing
// this surface does with the answer is say it.
type CostShareReader interface {
	CostShare(ctx context.Context) agentquota.Reading
}

// WithCostShare installs the reader behind the cost warning. A registry without
// one raises no such warning, which is every composition that meters no cost.
func WithCostShare(reader CostShareReader) RegistryOption {
	return func(r *Registry) { r.cost = reader }
}

// warningCostShare marks an answer produced while this credential was past its
// share of the workspace's AI budget.
const warningCostShare = "ai_budget_share_spent"

// noteCostShare says so on the answer when this agent is past its share.
//
// SAYING is the whole control. MCP-SESS-COST is soft by the spec's own word, so
// there is no refusal to make and no window to wait for — what the quota exists
// to produce is the fact being VISIBLE to the human reading the answer, and a
// counter nobody is ever shown is a counter that governs nothing.
//
// Raised after the handler and before the envelope is sealed, because the
// warning belongs to the answer this call produced rather than to the next one.
func (r *Registry) noteCostShare(ctx context.Context) {
	if r.cost == nil {
		return
	}
	reading := r.cost.CostShare(ctx)
	if !reading.Exceeded {
		return
	}
	noteWarning(ctx, warningCostShare, fmt.Sprintf(
		"This agent has spent %d of the %d model tokens that are its share of this workspace's AI budget for the current window. "+
			"Nothing is being withheld; the workspace's own budget guardrail is what acts on overspend.",
		reading.Observed, reading.Limit))
}

// ChargeAdmittedCall and ChargeEffect are the SECOND door's charge points
// (ADR-0055): a mutating REST call admits against its tool twin's spec but
// never invokes this registry, so without these the counters the gate reads on
// that door are counters nothing pays into — every threshold sits at zero
// forever and a credential that never touches /mcp is unbounded.
//
// They are exported, and they are the only exported charge points, because the
// composition layer owns that door and this package may not import it. They
// mirror Invoke's own two moments exactly: the ceiling before anything happens,
// the act after it has.
//
// ChargeAdmittedCall runs after admission and before the handler, and REFUSES
// what it cannot count, for the same reason chargeCall does.
func (r *Registry) ChargeAdmittedCall(ctx context.Context, spec mcp.ToolSpec) error {
	return r.chargeCall(ctx, spec, nothingHasHappenedYet)
}

// ChargeServedRecords records what ONE REST response handed over against
// MCP-SESS-READS. It is the read twin of ChargeAdmittedCall and exists for the
// same reason: the composition layer owns that door and this package may not
// import it, so without a charge point here AdmitRead refuses on a counter
// nothing increments and a credential reading only over /v1 is unbounded.
//
// It counts RECORDS, which is what the counter measures and what the MCP door
// charges at chargeAnswer — a page of one and a page of two hundred are not the
// same read, and a per-request charge would price them alike.
//
// It REPORTS a failed charge rather than deciding what it costs. That decision
// is this file's one rule — refuse only while nothing has happened yet — and on
// the REST door only the caller knows which side of it this response is on: a
// read has committed nothing, while a mutation's body is written after its
// effect landed.
func (r *Registry) ChargeServedRecords(ctx context.Context, n int) error {
	if r.quota == nil || n <= 0 {
		return nil
	}
	if err := r.quota.Consume(ctx, agentquota.Reads, n); err != nil {
		slog.ErrorContext(ctx, "recording the records a REST response served against the read bound failed",
			"quota", string(agentquota.Reads), "records", n, "err", err)
		return fmt.Errorf(
			"crmagents: %d records could not be counted against this agent's read bound: %w",
			n, apperrors.ErrBudgetExceeded)
	}
	return nil
}

// ChargeRedeemedCall is ChargeAdmittedCall for a call whose approval has
// already been consumed. It never refuses, for the reason refusable states.
func (r *Registry) ChargeRedeemedCall(ctx context.Context, spec mcp.ToolSpec) {
	//craft:ignore swallowed-errors chargeCall cannot fail on this path — it logs and returns nil once an approval is consumed
	_ = r.chargeCall(ctx, spec, anApprovalWasConsumed)
}

// ChargeEffect records a completed mutating REST call against the counter its
// kind names. It takes no record count: the REST read path is charged
// separately and per record where records leave that surface, and a mutation's
// own read-back is not this call's to count twice.
//
// It never fails the caller. By the time it runs the effect has committed —
// the handler has already answered — so an uncounted write is a small
// accounting loss where a reported failure would invite the retry of something
// that already happened.
func (r *Registry) ChargeEffect(ctx context.Context, spec mcp.ToolSpec) {
	if r.quota == nil {
		return
	}
	act := agentquota.CounterFor(spec)
	if act == agentquota.Reads {
		return
	}
	if err := r.quota.Consume(ctx, act, 1); err != nil {
		slog.ErrorContext(ctx, "recording a completed REST effect against its quota failed",
			"tool", spec.Name, "quota", string(act), "err", err)
	}
}
