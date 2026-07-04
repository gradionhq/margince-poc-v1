package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/crmctx"
	"github.com/gradionhq/margince/backend/kernel/errs"
	"github.com/gradionhq/margince/backend/mcp"
)

func agentCtx(scopes ...crmctx.Scope) context.Context {
	// A full seat by default: the scope/tier tests exercise the gate's
	// scope∧tier logic, not the seat ceiling, which has its own tests.
	return seatAgentCtx(crmctx.SeatFull, scopes...)
}

func seatAgentCtx(seat crmctx.SeatType, scopes ...crmctx.Scope) context.Context {
	return crmctx.WithActor(context.Background(), crmctx.Principal{
		Type: crmctx.PrincipalAgent, ID: "agent:test", SeatType: seat,
		Scopes: crmctx.NewScopeSet(scopes...),
	})
}

func noResolve() (mcp.TierResolverInput, error) { return mcp.TierResolverInput{}, nil }

func TestScopeIsCheckedBeforeTier(t *testing.T) {
	spec := mcp.ToolSpec{Name: "send_email", RequiredScope: crmctx.ScopeSend, Tier: mcp.TierYellow}

	err := Admit(agentCtx(crmctx.ScopeRead, crmctx.ScopeWrite), spec, noResolve)
	if !errors.Is(err, errs.ErrScopeExceeded) {
		t.Fatalf("missing scope → %v, want ErrScopeExceeded (never ErrRequiresApproval: an out-of-scope caller must not learn the tier)", err)
	}
}

func TestYellowIsBlockedBehindApproval(t *testing.T) {
	spec := mcp.ToolSpec{Name: "archive_record", RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierYellow}
	if err := Admit(agentCtx(crmctx.ScopeWrite), spec, noResolve); !errors.Is(err, errs.ErrRequiresApproval) {
		t.Fatalf("🟡 without approval → %v, want ErrRequiresApproval", err)
	}
}

func TestGreenIsAdmitted(t *testing.T) {
	spec := mcp.ToolSpec{Name: "read_record", RequiredScope: crmctx.ScopeRead, Tier: mcp.TierGreen}
	if err := Admit(agentCtx(crmctx.ScopeRead), spec, noResolve); err != nil {
		t.Fatalf("🟢 in scope → %v, want admitted", err)
	}
}

func TestDynamicTierIsResolvedPerCall(t *testing.T) {
	resolver := func(in mcp.TierResolverInput) mcp.RiskTier {
		if in.TargetStageSemantic == "open" {
			return mcp.TierGreen
		}
		return mcp.TierYellow
	}
	spec := mcp.ToolSpec{
		Name: "advance_deal", RequiredScope: crmctx.ScopeWrite,
		Tier: mcp.TierDynamic, TierResolver: resolver,
	}

	open := func() (mcp.TierResolverInput, error) {
		return mcp.TierResolverInput{TargetStageSemantic: "open"}, nil
	}
	if err := Admit(agentCtx(crmctx.ScopeWrite), spec, open); err != nil {
		t.Fatalf("open→open resolves 🟢: %v", err)
	}

	won := func() (mcp.TierResolverInput, error) {
		return mcp.TierResolverInput{TargetStageSemantic: "won"}, nil
	}
	if err := Admit(agentCtx(crmctx.ScopeWrite), spec, won); !errors.Is(err, errs.ErrRequiresApproval) {
		t.Fatalf("move to won → %v, want ErrRequiresApproval (the always-🟡 floor)", err)
	}
}

func TestDynamicWithoutResolverFailsClosed(t *testing.T) {
	spec := mcp.ToolSpec{Name: "broken", RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierDynamic}
	err := Admit(agentCtx(crmctx.ScopeWrite), spec, noResolve)
	if err == nil || errors.Is(err, errs.ErrRequiresApproval) {
		t.Fatalf("mis-registered dynamic spec → %v, want a hard failure, not a tier decision", err)
	}
}

func TestHumansDoNotRideTheScopeModel(t *testing.T) {
	ctx := crmctx.WithActor(context.Background(), crmctx.Principal{
		Type: crmctx.PrincipalHuman, ID: "human:u1",
	})
	spec := mcp.ToolSpec{Name: "archive_record", RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierYellow}
	if err := Admit(ctx, spec, noResolve); err != nil {
		t.Fatalf("human through the gate → %v; human authority is their RBAC, enforced at the store", err)
	}
}

func TestReadSeatAgentCannotMutate(t *testing.T) {
	// A read seat (or an agent acting for one) may run read tools but never
	// a write/send tool — refused with the seat sentinel, BEFORE the tier is
	// consulted, and never staged for approval (no approval lifts a
	// licensing ceiling). A62/ADR-0047.
	write := mcp.ToolSpec{Name: "create_record", RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierGreen}
	err := Admit(seatAgentCtx(crmctx.SeatRead, crmctx.ScopeRead, crmctx.ScopeWrite), write, noResolve)
	if !errors.Is(err, errs.ErrSeatTierInsufficient) {
		t.Fatalf("read-seat write → %v, want ErrSeatTierInsufficient", err)
	}
	if errors.Is(err, errs.ErrRequiresApproval) {
		t.Fatal("a read-seat mutation must not be staged for approval")
	}

	read := mcp.ToolSpec{Name: "read_record", RequiredScope: crmctx.ScopeRead, Tier: mcp.TierGreen}
	if err := Admit(seatAgentCtx(crmctx.SeatRead, crmctx.ScopeRead), read, noResolve); err != nil {
		t.Fatalf("read-seat read tool → %v, want admitted", err)
	}
}

func TestUnsetSeatFailsClosed(t *testing.T) {
	// An agent whose seat was not resolved must not mutate on the strength
	// of the omission.
	write := mcp.ToolSpec{Name: "create_record", RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierGreen}
	if err := Admit(seatAgentCtx("", crmctx.ScopeWrite), write, noResolve); !errors.Is(err, errs.ErrSeatTierInsufficient) {
		t.Fatalf("unset seat write → %v, want ErrSeatTierInsufficient (fail-closed)", err)
	}
}

func TestNoPrincipalIsRefused(t *testing.T) {
	spec := mcp.ToolSpec{Name: "read_record", RequiredScope: crmctx.ScopeRead, Tier: mcp.TierGreen}
	if err := Admit(context.Background(), spec, noResolve); err == nil {
		t.Fatal("anonymous context admitted")
	}
}
