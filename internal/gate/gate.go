// Package gate is the ONE admission point for governed tool calls
// (interfaces.md §2, api-rate-limits §2.2): scope ∧ tier, resolved
// against the calling Principal, BEFORE any handler runs. It is its own
// package so nothing else can mint an admitted capability — Surface A
// (inbound agents) and Surface B (our own runner) both enter here, and
// there is no other door.
package gate

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/kernel/errs"
	"github.com/gradionhq/fable-poc/mcp"
)

// Admit decides whether the context's principal may run the tool with
// these arguments. resolve supplies the TierResolverInput for dynamic
// tools — it is called lazily, only when the spec is TierDynamic, because
// building it may cost a database read (the target stage's semantic).
//
// The decision order is deliberate: scope first (a caller without the
// verb never learns the tier), then tier. A 🟡 outcome (declared or
// dynamically resolved) returns ErrRequiresApproval — the approval-token
// redemption path is EP07's; until it lands, 🟡 tools are structurally
// blocked for agents rather than quietly allowed.
func Admit(ctx context.Context, spec mcp.ToolSpec, resolve func() (mcp.TierResolverInput, error)) error {
	p, ok := crmctx.Actor(ctx)
	if !ok {
		return errors.New("gate: no principal bound to context")
	}

	// Humans and the system principal do not ride the gate's scope model:
	// their authority is their RBAC, enforced at the store. The gate
	// exists to bound AGENTS (03b Layer 1); a human reaching a tool
	// through the UI already answered for the action.
	if p.Type != crmctx.PrincipalAgent {
		return nil
	}

	if !p.Scopes.Has(spec.RequiredScope) {
		return fmt.Errorf("gate: %s needs scope %q: %w", spec.Name, spec.RequiredScope, errs.ErrScopeExceeded)
	}

	// The seat ceiling is checked before tier (A62/ADR-0047): a read seat —
	// or an agent acting for one — may run only read-scoped tools, whatever
	// their passport scope or the target's tier would otherwise permit. A
	// non-read tool is refused outright, never staged for approval, because
	// no approval can lift a licensing ceiling.
	if spec.RequiredScope != crmctx.ScopeRead && !p.SeatType.CanMutate() {
		return fmt.Errorf("gate: %s needs a full seat; %s acts for a read seat: %w",
			spec.Name, p.ID, errs.ErrSeatTierInsufficient)
	}

	tier := spec.Tier
	if tier == mcp.TierDynamic {
		if spec.TierResolver == nil {
			// Registration should have refused this spec; failing closed
			// here keeps a mis-registered tool from defaulting to 🟢.
			return fmt.Errorf("gate: %s is TierDynamic without a resolver", spec.Name)
		}
		in, err := resolve()
		if err != nil {
			return err
		}
		tier = spec.TierResolver(in)
	}
	if tier != mcp.TierGreen {
		return fmt.Errorf("gate: %s is a confirm-first (🟡) action: %w", spec.Name, errs.ErrRequiresApproval)
	}
	return nil
}
