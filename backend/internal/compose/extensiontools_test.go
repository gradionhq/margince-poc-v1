// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
// unitToolDescription is the stand-in selection prose a declared tool carries
// so the composition will serve it. A served tool with no description is
// refused; every unit tool here is declared to exercise something else.
const unitToolDescription = "A stand-in unit tool, described so the composition has something to serve."

func TestBuildExtensionToolsAdaptsHandlerBearingTools(t *testing.T) {
	exts := []extension.Extension{{
		Name:    "demo",
		Version: "1.0.0",
		Tools: []extension.Tool{
			{
				Name: "served", Description: unitToolDescription, Version: "1.0.0",
				Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"ok":true}`), nil
				},
			},
			{Name: "inert", Description: unitToolDescription, Version: "1.0.0", Tier: extension.TierConfirmationRequired, RequestedScope: extension.ScopeWrite},
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
	// The unit's own words, not a placeholder the adapter could have supplied
	// to satisfy the refusal: a description substituted here would be listed
	// beside the core surface as if the unit had written it.
	if spec.Description != unitToolDescription {
		t.Fatalf("declared Description not carried to the served spec: %q", spec.Description)
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
			Name: "archive", Description: unitToolDescription, Version: "1.0.0",
			Tier: extension.TierConfirmationRequired, RequestedScope: extension.ScopeWrite,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "confirmation-required tool is not yet supported") {
		t.Fatalf("err = %v, want the served-🟡 rejection", err)
	}
}

// TestBuildExtensionToolsRejectsCrossUnitServedNameCollision: the tool
// registry's namespace is global, so two units serving the same name is a
// wiring conflict. It must fail while building the set — before any
// jurisdiction is applied — not surface later as a Register panic.
func TestBuildExtensionToolsRejectsCrossUnitServedNameCollision(t *testing.T) {
	served := extension.Tool{
		Name: "quote", Description: unitToolDescription, Version: "1.0.0",
		Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
		Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
	}
	_, err := buildExtensionTools([]extension.Extension{
		{Name: "unit-a", Version: "1.0.0", Tools: []extension.Tool{served}},
		{Name: "unit-b", Version: "1.0.0", Tools: []extension.Tool{served}},
	})
	if err == nil || !strings.Contains(err.Error(), "both serve a tool named") {
		t.Fatalf("err = %v, want the cross-unit served-name collision", err)
	}
}

// TestBuildExtensionToolsRejectsAServedEgressTool: every core tool that
// leaves the workspace is 🟡, and a served extension tool cannot be — so an
// outbound one would auto-execute with no human in the loop and no operation
// declaring that this surface may reach outside at all. Both outbound caps,
// because `send` delivering and `enrich` fetching leave by the same door.
func TestBuildExtensionToolsRejectsAServedEgressTool(t *testing.T) {
	// A verb per cap, so each subtest reads as the act it refuses rather than
	// naming a delivery for the fetch case.
	outboundVerbs := map[extension.Scope]string{
		extension.ScopeSend:   "push_webhook",
		extension.ScopeEnrich: "fetch_profile",
	}
	for _, scope := range []extension.Scope{extension.ScopeSend, extension.ScopeEnrich} {
		t.Run(string(scope), func(t *testing.T) {
			_, err := buildExtensionTools([]extension.Extension{{
				Name: "demo", Version: "1.0.0",
				Tools: []extension.Tool{{
					Name: outboundVerbs[scope], Version: "1.0.0",
					Tier: extension.TierAutoExecute, RequestedScope: scope,
					Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
				}},
			}})
			if err == nil || !strings.Contains(err.Error(), "outbound") {
				t.Fatalf("err = %v, want the served-egress rejection", err)
			}
		})
	}
}

// TestBuildExtensionToolsDefaultsTheInputSchema: a tool that omits an input
// schema still advertises an object one (MCP requires it of every tool).
func TestBuildExtensionToolsDefaultsTheInputSchema(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "count_things", Description: unitToolDescription, Version: "1.0.0",
			Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tools[0].Spec().InputSchema); got != `{"type":"object"}` {
		t.Errorf("a tool without a declared input schema must advertise an object one, got %s", got)
	}
}

// TestBuildExtensionToolsRejectsAServedToolWithNoDescription: a title falls
// back to the verb because a verb is a serviceable label, but a description
// cannot fall back to the thing it exists to explain. A unit serving an
// undescribed tool would put it in the same listing as thirty core tools that
// each say what they are for, with nothing to choose it on.
func TestBuildExtensionToolsRejectsAServedToolWithNoDescription(t *testing.T) {
	_, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "give_quote", Version: "1.0.0",
			Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
			Handle: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "declares no Description") {
		t.Fatalf("err = %v, want the undescribed-served-tool rejection", err)
	}
}

// A handler-LESS declaration is a manifest request no client is ever shown, so
// the description it has no reader for is not required of it. Refusing one
// would make an operator-visible governance request fail over documentation
// nobody would read.
func TestBuildExtensionToolsAcceptsAnUndescribedInertTool(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "inert", Version: "1.0.0",
			Tier: extension.TierConfirmationRequired, RequestedScope: extension.ScopeWrite,
		}},
	}})
	if err != nil {
		t.Fatalf("an undescribed inert tool must still declare: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("an inert tool must serve nothing, got %d served", len(tools))
	}
}

// TestBuildExtensionToolsCarriesTheTitleAndFallsBackToTheVerb: a declared
// title reaches tools/list, and a unit that declares none is listed under its
// verb rather than registering a title-less spec (which the core registry
// refuses outright).
func TestBuildExtensionToolsCarriesTheTitleAndFallsBackToTheVerb(t *testing.T) {
	handle := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{
			{
				Name: "give_quote", Title: "Quote of the day", Description: unitToolDescription,
				Version: "1.0.0",
				Tier:    extension.TierAutoExecute, RequestedScope: extension.ScopeRead, Handle: handle,
			},
			{
				Name: "count_things", Description: unitToolDescription, Version: "1.0.0",
				Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead, Handle: handle,
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tools[0].Spec().Title; got != "Quote of the day" {
		t.Errorf("declared title = %q, want it carried to the served spec", got)
	}
	if got := tools[1].Spec().Title; got != "count_things" {
		t.Errorf("title-less tool = %q, want the verb as its display name", got)
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
			Name: "give_quote", Description: unitToolDescription, Version: "1.0.0",
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
	// The unit's own bytes, unchanged, inside the result envelope the registry
	// seals every answer into — an extension tool is governed and rendered
	// exactly like a core one, which is the property this asserts.
	var sealed struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("an extension tool's result is not an envelope: %v (%s)", err, out)
	}
	if got := string(sealed.Data); got != `{"quote":"it ain't over"}` {
		t.Fatalf("handler result not carried verbatim: %s", got)
	}
}

// TestComposedReadToolRequiresTheScope: admission is real — the same tool
// is refused when the principal lacks the requested scope.
func TestComposedReadToolRequiresTheScope(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "give_quote", Description: unitToolDescription, Version: "1.0.0",
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
	if _, err := r.Invoke(ctx, "give_quote", json.RawMessage(`{}`)); !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Fatalf("a scopeless principal must be denied with ErrScopeExceeded, got %v", err)
	}
}
