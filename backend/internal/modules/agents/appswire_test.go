// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The App extension as a client actually receives it: what reaches tools/list,
// what reaches both resource surfaces, and what a client that did not negotiate
// is served instead.
//
// Every assertion here runs against the real dispatcher and the real response
// bytes. A view is a document a host will fetch, sandbox and execute, so the
// question these tests answer is not "does the renderer work" but "is what the
// host receives the policy this server meant" — and only the wire can answer it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// viewURI is the document the tools in this file name.
const viewURI = "ui://margince/test-view.html"

// viewingTool is a read tool that names a view, which is the whole shape a
// UI-carrying tool has: an ordinary tool, plus one declaration.
type viewingTool struct{ name string }

func (t viewingTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: t.name, Title: "A tool with a view", Version: "v1",
		Description:   "Answers something, and can be rendered.",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		UI:          &mcp.ToolUI{ResourceURI: viewURI},
	}
}

func (t viewingTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// theView is the published descriptor the tools above point at, declaring the
// self-contained posture: no origin, no permission.
func theView() mcp.Resource {
	return mcp.Resource{
		URI: viewURI, Name: "test_view", Title: "Test view",
		Description: "a view", MIMEType: mcp.AppMIMEType,
		RequiredScope: principal.ScopeRead,
		UI:            &mcp.ResourceUI{PrefersBorder: true},
	}
}

// dispatcherServingAView wires one UI tool and the document it names, which is
// the minimum surface on which the extension is real.
func dispatcherServingAView(t *testing.T) *Dispatcher {
	t.Helper()
	registry := NewRegistry(nil, nil)
	registry.Register(viewingTool{name: "read_something"})
	return NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").
		WithLogger(discardLog()).
		WithResources(stubResources{
			published: []mcp.Resource{theView()},
			contents: map[string]mcp.ResourceContents{
				// The policy rides the CONTENTS, exactly as the real view
				// provider sends it — a stub that omitted it would be asserting
				// against a provider production does not have, and the read
				// path's own policy rendering would go untested.
				viewURI: {
					URI: viewURI, MIMEType: mcp.AppMIMEType, Text: "<!doctype html><title>t</title>",
					UI: &mcp.ResourceUI{PrefersBorder: true},
				},
			},
		})
}

// A request that declared the extension is told which document renders the
// tool. Without this member a host has no way to find a view at all, so this is
// the extension's whole tool-side contract.
func TestANegotiatedRequestIsToldWhichViewRendersATool(t *testing.T) {
	d := dispatcherServingAView(t)
	listed := d.toolList(agentHolding(principal.ScopeRead), framing{modern: true, apps: true})
	if len(listed) != 1 {
		t.Fatalf("tools/list returned %d entries, want the one registered tool", len(listed))
	}
	encoded, err := json.Marshal(listed[0])
	if err != nil {
		t.Fatalf("encoding the listed tool: %v", err)
	}
	if !strings.Contains(string(encoded), `"_meta":{"ui":{"resourceUri":"`+viewURI+`"`) {
		t.Errorf("a negotiated tools/list does not name the tool's view:\n%s", encoded)
	}
}

// A request that did NOT declare the extension is served the tool with no view
// on it. Offering one would hand a client a document it never said it could
// render, and the legacy framing cannot decline because it has no place to
// declare anything.
func TestAnUnnegotiatedRequestIsServedNoView(t *testing.T) {
	d := dispatcherServingAView(t)
	for _, tc := range []struct {
		name string
		fr   framing
	}{
		{"the handshake era, which cannot negotiate at all", legacyFraming},
		{"the modern era with the extension undeclared", framing{modern: true}},
		{"the modern era declaring only Tasks", framing{modern: true, tasks: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := d.toolList(agentHolding(principal.ScopeRead), tc.fr)
			if len(listed) != 1 {
				t.Fatalf("tools/list returned %d entries", len(listed))
			}
			if _, offered := listed[0][fieldMeta]; offered {
				t.Error("a request that did not declare the App extension was offered a view anyway")
			}
		})
	}
}

// A server whose tools carry no view must not claim the extension, even to a
// client that declared it: the claim entitles a host to a document to prefetch,
// and there is none.
func TestAServerWithNoViewsClaimsNoAppExtension(t *testing.T) {
	registry := NewRegistry(nil, nil)
	registry.Register(echoTool{spec: mcp.ToolSpec{
		Name: "read_record", Title: "Read", Version: "v1", Description: "Reads.",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, out: json.RawMessage(`{}`)})
	d := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())

	if d.appsServed() {
		t.Fatal("a surface whose tools carry no view reports that it serves views")
	}
	if extensions, claimed := d.capabilities(true)["extensions"].(map[string]any); claimed {
		if _, advertised := extensions[extensionUI]; advertised {
			t.Error("the App extension is advertised by a server with no view to serve")
		}
	}
	// And the member is withheld even from a caller that asked for it, because
	// the two halves are independent — a declaration cannot conjure a document.
	listed := d.toolList(agentHolding(principal.ScopeRead), framing{modern: true, apps: true})
	if _, offered := listed[0][fieldMeta]; offered {
		t.Error("a viewless server offered _meta anyway to a negotiating client")
	}
}

// A server that DOES serve views advertises the extension — but only in the era
// that can negotiate one. Advertising to the handshake era offers a negotiation
// the client has no way to enter, which is why Tasks is era-gated too.
func TestTheAppExtensionIsAdvertisedOnlyToTheEraThatCanNegotiateIt(t *testing.T) {
	d := dispatcherServingAView(t)
	extensions, claimed := d.capabilities(true)["extensions"].(map[string]any)
	if !claimed {
		t.Fatal("a server serving views advertises no extensions at all")
	}
	if _, advertised := extensions[extensionUI]; !advertised {
		t.Errorf("the App extension is not advertised to a modern client: %v", extensions)
	}
	if _, leaked := d.capabilities(false)["extensions"]; leaked {
		t.Error("an extension is advertised to the handshake era, which has no way to declare one")
	}
}

// The sandbox policy reaches the host on the LISTING, unconditionally. The
// extension's premise is that a host may fetch and review a view before any
// tool call, so a policy withheld until negotiation would leave a prefetching
// host holding a document it has no rules for.
func TestTheListingCarriesEveryViewsSandboxPolicy(t *testing.T) {
	d := dispatcherServingAView(t)
	body, err := json.Marshal(rpc(t, d, "resources/list", "").Result)
	if err != nil {
		t.Fatalf("encoding resources/list: %v", err)
	}
	// The empty allowlists are asserted as PRESENT and empty. A host builds its
	// policy from these lists and admits no origin they do not name, so `[]`
	// instructs it to deny everything while a missing or null list is a rule it
	// was never given.
	for _, want := range []string{
		`"_meta":{"ui":{`,
		`"connectDomains":[]`,
		`"resourceDomains":[]`,
		`"frameDomains":[]`,
		`"baseUriDomains":[]`,
		`"prefersBorder":true`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("resources/list is missing %s:\n%s", want, body)
		}
	}
	// Narrowed to the four lists this is about. A blanket null check would fail
	// on any legitimate null elsewhere in the response and read as this defect.
	for _, list := range []string{"connectDomains", "resourceDomains", "frameDomains", "baseUriDomains"} {
		if strings.Contains(string(body), `"`+list+`":null`) {
			t.Errorf("%s reached the host as null, which is a rule it was never given rather than a denial:\n%s", list, body)
		}
	}
}

// And the same policy reaches a host that read the document by URI without
// listing first. A host may ask either way, and a policy present on only one
// surface is a policy that depends on the order it happened to ask in.
func TestTheReadCarriesTheSamePolicyAsTheListing(t *testing.T) {
	d := dispatcherServingAView(t)
	read, err := json.Marshal(rpc(t, d, "resources/read", `{"uri":"`+viewURI+`"}`).Result)
	if err != nil {
		t.Fatalf("encoding resources/read: %v", err)
	}
	if !strings.Contains(string(read), `"_meta":{"ui":{`) {
		t.Errorf("resources/read carries no sandbox policy:\n%s", read)
	}
	if !strings.Contains(string(read), `"connectDomains":[]`) {
		t.Errorf("resources/read carries no origin allowlist:\n%s", read)
	}
}

// An ordinary document carries no view policy on either surface. A sandbox
// declaration on something no host will sandbox is a claim about nothing, and
// it would make every JSON resource look like an App to a host dispatching on
// the member's presence.
func TestAnOrdinaryDocumentCarriesNoViewPolicy(t *testing.T) {
	d := dispatcherWith(stubResources{
		published: []mcp.Resource{{
			URI: "margince://schema/query", Name: "query_vocabulary", Title: "Vocabulary",
			Description: "what you may ask", MIMEType: "application/json",
			RequiredScope: principal.ScopeRead,
		}},
		contents: map[string]mcp.ResourceContents{
			"margince://schema/query": {
				URI: "margince://schema/query", MIMEType: "application/json", Text: `{}`,
			},
		},
	})
	for _, tc := range []struct{ method, params string }{
		{"resources/list", ""},
		{"resources/read", `{"uri":"margince://schema/query"}`},
	} {
		body, err := json.Marshal(rpc(t, d, tc.method, tc.params).Result)
		if err != nil {
			t.Fatalf("encoding %s: %v", tc.method, err)
		}
		if strings.Contains(string(body), "_meta") {
			t.Errorf("%s put a view's policy on an ordinary document:\n%s", tc.method, body)
		}
	}
}

// A view the caller may not read is invisible on both surfaces, exactly as an
// ordinary document is. A view is still a document, and the existence-hiding
// the resource surface applies is not something the extension may opt out of.
func TestAViewTheCallerMayNotReadStaysInvisible(t *testing.T) {
	view := theView()
	view.RequiredScope = principal.ScopeWrite
	d := dispatcherWith(stubResources{
		published: []mcp.Resource{view},
		contents: map[string]mcp.ResourceContents{
			viewURI: {
				URI: viewURI, MIMEType: mcp.AppMIMEType, Text: "<!doctype html>",
				UI: &mcp.ResourceUI{PrefersBorder: true},
			},
		},
	})
	ctx := agentHolding(principal.ScopeRead)

	listed := decodeResult[resourceListResult](t, rpcAs(ctx, t, d, "resources/list", ""))
	if len(listed.Resources) != 0 {
		t.Errorf("a view outside the caller's scopes was advertised anyway: %+v", listed.Resources)
	}
	answer := rpcAs(ctx, t, d, "resources/read", `{"uri":"`+viewURI+`"}`)
	if answer.Error == nil || answer.Error.Code != resourceNotFound {
		t.Errorf("reading a view outside the caller's scopes answered %+v, want the same not-found an unknown URI gets", answer.Error)
	}
}
