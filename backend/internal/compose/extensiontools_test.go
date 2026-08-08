// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

// unitVerb is the CONTRACT half of a unit tool under test — what the unit's
// api/ fragment declares and gen-composition re-emits into the composition as a
// literal. After the narrowing, every governance field these tests used to
// spell inside an extension.Tool literal is spelled here instead, because that
// is where a unit author now spells it.
func unitVerb(unit, tool string, tier extension.Tier, scope extension.Scope) extension.Verb {
	return extension.Verb{
		Unit:           extension.Name(unit),
		Contract:       "crm.yaml",
		OperationID:    tool + "Op",
		Route:          "/ext/" + unit + "/" + strings.ReplaceAll(tool, "_", "-"),
		Method:         http.MethodPost,
		Tool:           tool,
		Description:    unitToolDescription,
		Version:        "1.0.0",
		Tier:           tier,
		RequestedScope: scope,
	}
}

// servedHandle is a handler that returns nothing; the tests using it are about
// the DECLARATION being accepted or refused, never about behavior.
func servedHandle(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestBuildExtensionToolsAdaptsHandlerBearingTools(t *testing.T) {
	exts := []extension.Extension{{
		Name:    "demo",
		Version: "1.0.0",
		Tools: []extension.Tool{
			{
				Name: "served",
				Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"ok":true}`), nil
				},
			},
		},
	}}
	servedVerb := unitVerb("demo", "served", extension.TierAutoExecute, extension.ScopeRead)
	servedVerb.InputSchema = json.RawMessage(`{"type":"object"}`)
	// "inert" is declared by the contract and by NOTHING in Go — the shape a
	// contract-only governed request takes now that a Tool is {Name, Handle}.
	tools, err := buildExtensionTools(exts, []extension.Verb{
		servedVerb,
		unitVerb("demo", "inert", extension.TierConfirmationRequired, extension.ScopeWrite),
	})
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
		Tools: []extension.Tool{{Name: "archive", Handle: servedHandle}},
	}}, []extension.Verb{unitVerb("demo", "archive", extension.TierConfirmationRequired, extension.ScopeWrite)})
	if err == nil || !strings.Contains(err.Error(), "confirmation-required tool is not yet supported") {
		t.Fatalf("err = %v, want the served-🟡 rejection", err)
	}
}

// TestBuildExtensionToolsRejectsCrossUnitServedNameCollision: the tool
// registry's namespace is global, so two units serving the same name is a
// wiring conflict. It must fail while building the set — before any
// jurisdiction is applied — not surface later as a Register panic.
func TestBuildExtensionToolsRejectsCrossUnitServedNameCollision(t *testing.T) {
	served := extension.Tool{Name: "quote", Handle: servedHandle}
	_, err := buildExtensionTools([]extension.Extension{
		{Name: "unit-a", Version: "1.0.0", Tools: []extension.Tool{served}},
		{Name: "unit-b", Version: "1.0.0", Tools: []extension.Tool{served}},
	}, []extension.Verb{
		unitVerb("unit-a", "quote", extension.TierAutoExecute, extension.ScopeRead),
		unitVerb("unit-b", "quote", extension.TierAutoExecute, extension.ScopeRead),
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
				Tools: []extension.Tool{{Name: outboundVerbs[scope], Handle: servedHandle}},
			}}, []extension.Verb{unitVerb("demo", outboundVerbs[scope], extension.TierAutoExecute, scope)})
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
		Tools: []extension.Tool{{Name: "count_things", Handle: servedHandle}},
	}}, []extension.Verb{unitVerb("demo", "count_things", extension.TierAutoExecute, extension.ScopeRead)})
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
	undescribed := unitVerb("demo", "give_quote", extension.TierAutoExecute, extension.ScopeRead)
	undescribed.Description = ""
	_, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{Name: "give_quote", Handle: servedHandle}},
	}}, []extension.Verb{undescribed})
	if err == nil || !strings.Contains(err.Error(), "declares no Description") {
		t.Fatalf("err = %v, want the undescribed-served-tool rejection", err)
	}
}

// A handler-LESS declaration is a manifest request no client is ever shown, so
// the description it has no reader for is not required of it. Refusing one
// would make an operator-visible governance request fail over documentation
// nobody would read.
func TestBuildExtensionToolsAcceptsAnUndescribedInertTool(t *testing.T) {
	undescribed := unitVerb("demo", "inert", extension.TierConfirmationRequired, extension.ScopeWrite)
	undescribed.Description = ""
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
	}}, []extension.Verb{undescribed})
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
	titled := unitVerb("demo", "give_quote", extension.TierAutoExecute, extension.ScopeRead)
	titled.Title = "Quote of the day"
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{
			{Name: "give_quote", Handle: servedHandle},
			{Name: "count_things", Handle: servedHandle},
		},
	}}, []extension.Verb{titled, unitVerb("demo", "count_things", extension.TierAutoExecute, extension.ScopeRead)})
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
			Name: "give_quote",
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"quote":"it ain't over"}`), nil
			},
		}},
	}}, []extension.Verb{unitVerb("demo", "give_quote", extension.TierAutoExecute, extension.ScopeRead)})
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
			Name: "give_quote",
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}},
	}}, []extension.Verb{unitVerb("demo", "give_quote", extension.TierAutoExecute, extension.ScopeRead)})
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
