// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package auth is the ONE admission point for governed agent actions
// (interfaces.md §2, ADR-0055): scope ∧ seat ∧ tier ∧ quota, resolved against
// the calling Principal, BEFORE any handler runs — whether the action arrives
// as an MCP tool call or a mutating REST operation. It is its own package
// so nothing else can mint an admitted capability — Surface A (inbound
// agents) and Surface B (our own runner) both enter here, and there is no
// other door.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/readmeter"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ReadBound answers whether the calling agent has been handed more records
// this window than MCP-SESS-READS allows. It is an interface rather than the
// concrete meter so this package — the one admission point — stays testable
// without a Redis client, and so a deployment that has not composed a bound is
// a visible nil rather than a meter that silently answers "plenty".
type ReadBound interface {
	Read(ctx context.Context) readmeter.Reading
}

// Gate admits governed actions. Its authority source is the
// shared/ports/authz seam: identity implements it, the composition root
// injects it, and Admit re-derives the granting human's seat + RBAC live
// at every admission — so a revocation binds mid-session (RT-AR-M11)
// instead of surviving on whatever the transport stamped earlier.
type Gate struct {
	authority authz.Resolver
	reads     ReadBound
}

// GateOption configures a Gate at construction. Variadic rather than
// positional so the six existing composition sites are not rewritten every
// time the gate grows a dependency, which is how one of them ends up passing
// nil by copy-paste.
type GateOption func(*Gate)

// WithReadBound installs the MCP-SESS-READS meter. A gate built WITHOUT one
// does not enforce the read bound at all, which is a real composition rather
// than an oversight: the Surface-B runner and the workflow paths build a gate
// and run as the human or system that started them, whom this bound does not
// govern. The api server — the one role an agent principal ever reaches —
// passes it, from the same pointer it gives the registry that charges it.
func WithReadBound(reads ReadBound) GateOption {
	return func(g *Gate) { g.reads = reads }
}

// NewGate builds the admission point over its authority seam. Options add
// the dependencies only some roles have — today, the read bound.
func NewGate(authority authz.Resolver, opts ...GateOption) *Gate {
	g := &Gate{authority: authority}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Admit decides whether the context's principal may run the action with
// spec's tier model. resolve supplies the TierResolverInput for dynamic
// tools — called lazily, only when the spec is TierDynamic, because
// building it may cost a database read (the target stage's semantic).
//
// The decision order is deliberate: scope first (a caller without the
// verb never learns the tier, and pays no authority read), then the live
// seat ceiling, then the read quota, then tier. A 🟡 outcome (declared or
// dynamically resolved) returns ErrRequiresApproval. On admission the returned
// context carries the principal refreshed with the re-derived authority,
// so downstream store RBAC runs on current grants.
func (g *Gate) Admit(ctx context.Context, spec mcp.ToolSpec, resolve func() (mcp.TierResolverInput, error)) (context.Context, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return ctx, errors.New("gate: no principal bound to context")
	}

	// Humans and the system principal do not ride the gate's scope model:
	// their authority is their RBAC, enforced at the store. The gate
	// exists to bound AGENTS (03b Layer 1); a human reaching a tool
	// through the UI already answered for the action.
	if p.Type != principal.PrincipalAgent {
		return ctx, nil
	}

	if !p.Scopes.Has(spec.RequiredScope) {
		return ctx, fmt.Errorf("gate: %s needs scope %q: %w", spec.Name, spec.RequiredScope, apperrors.ErrScopeExceeded)
	}

	// Re-derive the granting human's authority through the seam — never
	// trust the principal's stamped copy for an admission decision. A
	// gate composed without a resolver, or an agent without a granting
	// human, fails closed: absence of authority data is denial.
	if g == nil || g.authority == nil {
		return ctx, fmt.Errorf("gate: %s: no authority resolver composed: %w", spec.Name, apperrors.ErrPermissionDenied)
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok || p.OnBehalfOf.IsZero() {
		return ctx, fmt.Errorf("gate: %s: agent principal lacks workspace or granting human: %w", spec.Name, apperrors.ErrPermissionDenied)
	}
	seat, err := g.authority.SeatType(ctx, wsID, p.OnBehalfOf)
	if err != nil {
		return ctx, deniedIfGone("seat", spec.Name, err)
	}
	rbac, err := g.authority.EffectiveRBAC(ctx, wsID, p.OnBehalfOf)
	if err != nil {
		return ctx, deniedIfGone("rbac", spec.Name, err)
	}
	p.SeatType, p.Permissions, p.TeamIDs = seat, rbac.Permissions, rbac.TeamIDs
	ctx = principal.WithActor(ctx, p)

	// The seat ceiling is checked before tier (A62/ADR-0047): a read seat —
	// or an agent acting for one — may run only read-scoped tools, whatever
	// their passport scope or the target's tier would otherwise permit. A
	// non-read tool is refused outright, never staged for approval, because
	// no approval can lift a licensing ceiling.
	if spec.RequiredScope != principal.ScopeRead && !seat.CanMutate() {
		return ctx, fmt.Errorf("gate: %s needs a full seat; %s acts for a read seat: %w",
			spec.Name, p.ID, apperrors.ErrSeatTierInsufficient)
	}

	// Quota, the third term of "scope ∧ tier ∧ quota" (interfaces.md §2). It
	// runs AFTER scope and seat so a caller who may not run the verb at all
	// never learns that a quota exists, let alone how much of it is spent.
	//
	// It binds READ-scoped tools only. The bound is MCP-SESS-READS and it
	// counts records handed over; refusing a write because reading was heavy
	// would enforce a limit nobody wrote. A write that returns a record still
	// COUNTS toward the window (the charge point is where records leave the
	// surface, not here) — it is only the refusal that is read-scoped.
	if spec.RequiredScope == principal.ScopeRead && g.reads != nil {
		if reading := g.reads.Read(ctx); reading.Exceeded {
			return ctx, fmt.Errorf(
				"gate: %s: this agent has been handed %d records against a limit of %d for this window; it may read again when the window rolls, or once an operator raises the limit: %w",
				spec.Name, reading.Observed, reading.Limit, apperrors.ErrBudgetExceeded)
		}
	}

	tier := spec.Tier
	if tier == mcp.TierDynamic {
		if spec.TierResolver == nil {
			// Registration should have refused this spec; failing closed
			// here keeps a mis-registered tool from defaulting to 🟢.
			return ctx, fmt.Errorf("gate: %s is TierDynamic without a resolver", spec.Name)
		}
		in, err := resolve()
		if err != nil {
			return ctx, err
		}
		tier = spec.TierResolver(in)
	}
	if tier != mcp.TierAutoExecute {
		return ctx, fmt.Errorf("gate: %s is a confirm-first (🟡) action: %w", spec.Name, apperrors.ErrRequiresApproval)
	}
	return ctx, nil
}

// deniedIfGone maps a vanished granting human (revoked, archived,
// suspended) to a denial; infrastructure failures pass through so an
// outage reads as an error, never as an authorization answer.
func deniedIfGone(what, tool string, err error) error {
	if errors.Is(err, apperrors.ErrNotFound) {
		return fmt.Errorf("gate: %s: granting human no longer resolvable (%s): %w", tool, what, apperrors.ErrPermissionDenied)
	}
	return fmt.Errorf("gate: %s: resolving %s: %w", tool, what, err)
}

// AdmitRead applies the READ QUOTA alone, for a call that has no tool spec to
// admit against — the ADR-0055 REST surface's non-mutating half.
//
// It exists because that path has no tier to resolve and no scope to check
// against a spec (a GET's authority is the granting human's RBAC at the store),
// but it does spend the same bound. Without it a Passport that tripped the
// counter through the MCP door could keep reading the very same records through
// /v1 — one credential, two doors, one of them unbounded. ADR-0055's whole
// claim is that those two doors are governed alike.
//
// Non-agents are admitted untouched, exactly as Admit leaves them.
func (g *Gate) AdmitRead(ctx context.Context) error {
	if g == nil || g.reads == nil {
		return nil
	}
	p, ok := principal.Actor(ctx)
	if !ok || p.Type != principal.PrincipalAgent {
		return nil
	}
	if reading := g.reads.Read(ctx); reading.Exceeded {
		return fmt.Errorf(
			"gate: this agent has been handed %d records against a limit of %d for this window; it may read again when the window rolls, or once an operator raises the limit: %w",
			reading.Observed, reading.Limit, apperrors.ErrBudgetExceeded)
	}
	return nil
}
