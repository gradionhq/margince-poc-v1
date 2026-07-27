// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// fullSeat is a permissive gate authority: a full seat and empty RBAC, so
// admission turns purely on the tool's tier and requested scope — enough
// to exercise a 🟢 read tool end to end without a database.
type fullSeat struct{}

func (fullSeat) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{}, nil
}

func (fullSeat) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// TestBuildExtensionToolsAdaptsHandlerBearingTools: a tool with a handler
// becomes an mcp.Tool with the mapped tier/scope and its declared schemas;
// a handler-less (inert) tool is skipped — declared in the manifest, not
// served.
func TestBuildExtensionToolsAdaptsHandlerBearingTools(t *testing.T) {
	exts := []extension.Extension{{
		Name:    "demo",
		Version: "1.0.0",
		Tools: []extension.Tool{
			{
				Name: "served", Version: "1.0.0",
				Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"ok":true}`), nil
				},
			},
			{Name: "inert", Version: "1.0.0", Tier: extension.TierConfirmationRequired, RequestedScope: extension.ScopeWrite},
		},
	}}
	tools, err := buildExtensionTools(exts)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("want 1 served tool (the inert one skipped), got %d", len(tools))
	}
	spec := tools[0].Spec()
	if spec.Name != "served" || spec.Tier != mcp.TierAutoExecute || spec.RequiredScope != principal.ScopeRead {
		t.Fatalf("bad mapping: name=%q tier=%v scope=%v", spec.Name, spec.Tier, spec.RequiredScope)
	}
	if string(spec.InputSchema) != `{"type":"object"}` {
		t.Fatalf("declared InputSchema not carried to the served spec: %s", spec.InputSchema)
	}
}

// TestBuildExtensionToolsRejectsServedConfirmationRequired: a
// handler-bearing 🟡 tool cannot be served — the gate would refuse it on
// every call with no way to stage an approval — so building the set fails
// closed rather than registering a dead capability. (A handler-less 🟡
// tool is a manifest request, not served, and is fine.)
func TestBuildExtensionToolsRejectsServedConfirmationRequired(t *testing.T) {
	_, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "archive", Version: "1.0.0",
			Tier: extension.TierConfirmationRequired, RequestedScope: extension.ScopeWrite,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "confirmation-required tool is not yet supported") {
		t.Fatalf("err = %v, want the served-🟡 rejection", err)
	}
}

// TestBuildExtensionToolsDerivesEgressAndDefaultsSchema: a send-scoped tool
// is marked egress, and a tool that omits an input schema still advertises
// an object one (MCP requires it).
func TestBuildExtensionToolsDerivesEgressAndDefaultsSchema(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "push_webhook", Version: "1.0.0",
			Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeSend,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	spec := tools[0].Spec()
	if !spec.Egress {
		t.Error("a send-scoped tool must be marked egress")
	}
	if string(spec.InputSchema) != `{"type":"object"}` {
		t.Errorf("a tool without a declared input schema must advertise an object one, got %s", spec.InputSchema)
	}
}

// TestComposedToolServesThroughAdmission is the end-to-end proof: a
// composed 🟢/read tool registers into the same registry and admission
// gate as core tools, and Invoke reaches its handler.
func TestComposedToolServesThroughAdmission(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name:    "demo",
		Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "give_quote", Version: "1.0.0",
			Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"quote":"it ain't over"}`), nil
			},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	for _, tool := range tools {
		r.Register(tool)
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
	out, err := r.Invoke(ctx, "give_quote", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a 🟢 read tool held by a read-scoped principal must admit: %v", err)
	}
	if !strings.Contains(string(out), "quote") {
		t.Fatalf("handler result not returned: %s", out)
	}
}

// TestComposedReadToolRequiresTheScope: admission is real — the same tool
// is refused when the principal lacks the requested scope.
func TestComposedReadToolRequiresTheScope(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "give_quote", Version: "1.0.0",
			Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	for _, tool := range tools {
		r.Register(tool)
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(), // no read scope
	})
	if _, err := r.Invoke(ctx, "give_quote", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a scopeless principal must not reach the handler")
	}
}
