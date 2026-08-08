// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What this surface OWES the read bound (MCP-SESS-READS,
// api-rate-limits-and-abuse §2.2).
//
// The bound has two halves and they live apart on purpose. platform/auth
// decides admission on it — that is the ONE place a governed action is admitted,
// and a quota is an admission term ("scope ∧ tier ∧ quota"). This file is the
// other half: what an answer COSTS, charged where records leave the surface.
// Splitting them is what keeps a registry from being able to admit itself.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ReadCharger records records handed over against the caller's read bound.
// Registry only ever charges; reading the bound and refusing on it belongs to
// the one admission point, so this half of the seam cannot be used to decide
// anything.
type ReadCharger interface {
	Consume(ctx context.Context, n int) error
}

// RegistryOption configures a Registry at construction.
type RegistryOption func(*Registry)

// WithReadCharger installs the meter tool results are charged against. It is
// the same pointer compose hands the admission gate as its ReadBound, so the
// half that refuses and the half that pays can never end up counting against
// different windows. A registry without one records nothing — the composition
// no agent principal reaches (see auth.WithReadBound).
func WithReadCharger(reads ReadCharger) RegistryOption {
	return func(r *Registry) { r.reads = reads }
}

// chargeReads records what one answer handed over against MCP-SESS-READS, at
// the one place every tool's result passes through. Charging per TOOL instead
// would be a list to maintain, and the read tool added next is the one that
// forgets — which is exactly how a densely-joined answer becomes the cheapest
// bulk read on the surface (A139).
//
// It charges only after a SUCCESSFUL answer: the bound counts records the agent
// was actually given, and a handler that failed gave it none.
//
// A charge that cannot be recorded REFUSES a READ, and only a read. If the
// surface cannot count what it is about to hand over, it does not hand it over
// — the gate's rule on the way in, applied on the way out.
//
// A WRITE is served anyway, and the asymmetry is the whole point. By the time
// this runs the mutation has committed and any approval it redeemed is
// consumed: `send_email` has SENT. Withholding that result would report a
// completed, irreversible act as a failure, and the caller — reasonably —
// retries it. An uncounted read is a small accounting loss; a second email is
// not. So a write is logged and returned, and the read bound absorbs the
// undercount.
//
// Logging and serving a READ was tried and is wrong. It looks contained,
// because the gate fails closed while the counter is unreachable — but a charge
// lost to a TRANSIENT write error is lost for good: Redis recovers, the counter
// comes back short, and those records are read again for free. Every blip would
// quietly raise the ceiling.
func (r *Registry) chargeReads(ctx context.Context, spec mcp.ToolSpec, served int) error {
	if r.reads == nil || served <= 0 {
		return nil
	}
	err := r.reads.Consume(ctx, served)
	if err == nil {
		return nil
	}
	slog.ErrorContext(ctx, "recording served records against the read bound failed",
		"tool", spec.Name, "records", served, "read_only", spec.ReadOnly(), "err", err)
	if !spec.ReadOnly() {
		// The effect already happened. Reporting it as a failure is worse than
		// an uncounted read.
		return nil
	}
	return fmt.Errorf(
		"crmagents: %s read %d records that could not be counted against this agent's read bound, so the answer is withheld: %w",
		spec.Name, served, apperrors.ErrBudgetExceeded)
}
