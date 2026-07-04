package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// fullSeatAuthority satisfies the gate's live-authority seam with a full
// seat and no grants — enough for the registry's admission-order tests.
type fullSeatAuthority struct{}

func (fullSeatAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{}, nil
}
func (fullSeatAuthority) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

type fakeTool struct {
	spec    mcp.ToolSpec
	handled bool
}

func (f *fakeTool) Spec() mcp.ToolSpec { return f.spec }
func (f *fakeTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	f.handled = true
	return json.RawMessage(`{}`), nil
}

func mustPanic(t *testing.T, why string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("no panic: %s", why)
		}
	}()
	fn()
}

func TestRegisterRefusesAuthorityDefects(t *testing.T) {
	r := NewRegistry(nil, nil)
	r.Register(&fakeTool{spec: mcp.ToolSpec{Name: "read_record", Tier: mcp.TierGreen}})

	mustPanic(t, "duplicate name puts two handlers behind one admission decision", func() {
		r.Register(&fakeTool{spec: mcp.ToolSpec{Name: "read_record", Tier: mcp.TierGreen}})
	})
	mustPanic(t, "TierDynamic without a resolver has no computable tier", func() {
		r.Register(&fakeTool{spec: mcp.ToolSpec{Name: "advance", Tier: mcp.TierDynamic}})
	})
	mustPanic(t, "a resolver on a static tier would silently never run", func() {
		r.Register(&fakeTool{spec: mcp.ToolSpec{
			Name: "static", Tier: mcp.TierGreen,
			TierResolver: func(mcp.TierResolverInput) mcp.RiskTier { return mcp.TierGreen },
		}})
	})
}

func TestInvokeGatesBeforeHandle(t *testing.T) {
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	yellow := &fakeTool{spec: mcp.ToolSpec{Name: "archive_record", RequiredScope: principal.ScopeWrite, Tier: mcp.TierYellow}}
	r.Register(yellow)

	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeWrite),
	})
	if _, err := r.Invoke(ctx, "archive_record", json.RawMessage(`{}`)); err == nil {
		t.Fatal("🟡 call admitted")
	}
	if yellow.handled {
		t.Fatal("Handle ran despite the gate refusing — zero side effects is the contract")
	}

	var unknown *UnknownToolError
	if _, err := r.Invoke(ctx, "nope", nil); !errors.As(err, &unknown) {
		t.Fatalf("unknown tool → %v", err)
	}
}
