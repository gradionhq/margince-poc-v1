package crmagents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

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
	r := NewRegistry(nil)
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
	r := NewRegistry(nil)
	yellow := &fakeTool{spec: mcp.ToolSpec{Name: "archive_record", RequiredScope: principal.ScopeWrite, Tier: mcp.TierYellow}}
	r.Register(yellow)

	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t",
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
