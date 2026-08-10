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
	d := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").
		WithLogger(discardLog())
	// The other half of the same promise, wired the way compose wires it: the
	// document is not merely declared, it is one this server is HOLDING. Without
	// it every assertion below would be about a tool naming a document nothing
	// serves — which is the state the extension forbids.
	d.viewHeld = func(uri string) bool { return uri == viewURI }
	return d.
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

// The sandbox policy reaches the host on the LISTING, on every view listed.
// The extension's premise is that a host may fetch and review a view before
// any tool call, so a policy withheld until the first call would leave a
// prefetching host holding a document it has no rules for.
//
// Asked as an APP-DECLARING request, which is the only client this promise is
// about: a client that declared it cannot render a view is not shown one at
// all, so a policy on the document would be rules for something it will never
// draw.
func TestTheListingCarriesEveryViewsSandboxPolicy(t *testing.T) {
	d := dispatcherServingAView(t)
	body, err := json.Marshal(rpcRendering(t, d, "resources/list", "").Result)
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
	read, err := json.Marshal(rpcRendering(t, d, "resources/read", `{"uri":"`+viewURI+`"}`).Result)
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

// secondViewURI is the document the second tool below names, so partial
// availability can be driven: one view held, the other not.
const secondViewURI = "ui://margince/other-view.html"

type secondViewingTool struct{}

func (secondViewingTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_something_else", Title: "Another tool with a view", Version: "v1",
		Description:   "Answers something else, and can be rendered.",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		UI:          &mcp.ToolUI{ResourceURI: secondViewURI},
	}
}

func (secondViewingTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// listedNamed answers the listed entry for one tool, and whether it was listed
// at all — the second half matters because the point of these two tests is that
// a tool is NOT withdrawn when its view is missing.
func listedNamed(listed []map[string]any, name string) (map[string]any, bool) {
	for _, tool := range listed {
		if tool[fieldName] == name {
			return tool, true
		}
	}
	return nil, false
}

// THE INVARIANT TWO REVIEW ROUNDS FOUND BROKEN. A tool's UI.ResourceURI is a
// constant baked at registration, so a listing derived from the registry alone
// names a view whose document may never have arrived — and a host is entitled to
// prefetch what it is told about, which makes that a 404 and a panel that
// silently never appears.
//
// Partial availability is the case that matters, and it is the one a single-view
// test cannot see: one view missing must suppress exactly ONE tool's `_meta.ui`.
func TestAToolWhoseViewIsNotHeldLosesItsUIMetaButKeepsAnswering(t *testing.T) {
	registry := NewRegistry(nil, nil)
	registry.Register(viewingTool{name: "read_something"})
	registry.Register(secondViewingTool{})
	d := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
	// The first view arrived; the second did not.
	d.viewHeld = func(uri string) bool { return uri == viewURI }

	listed := d.toolList(agentHolding(principal.ScopeRead), framing{modern: true, apps: true})

	held, listedAtAll := listedNamed(listed, "read_something")
	if !listedAtAll {
		t.Fatal("the tool whose view IS held was not listed at all")
	}
	if _, carried := held[fieldMeta]; !carried {
		t.Error("the tool whose view is held carries no _meta.ui, so no host is told what renders it")
	}

	// The tool itself STAYS. A view is a second renderer for an answer this tool
	// already gives in text, never its only door — withdrawing the tool would
	// make the capability exist only where a document happened to load.
	unheld, stillListed := listedNamed(listed, "read_something_else")
	if !stillListed {
		t.Fatal("a tool whose view is missing was withdrawn from the catalog; the answer it gives in text is " +
			"the whole reason a view is allowed to be optional")
	}
	if _, carried := unheld[fieldMeta]; carried {
		t.Errorf("a tool names a view this server is not serving: %v", unheld[fieldMeta])
	}
}

// The same invariant stated over the whole surface rather than one pair, so a
// tool added later cannot quietly reintroduce it.
func TestNoToolNamesAViewTheServerDoesNotServe(t *testing.T) {
	registry := NewRegistry(nil, nil)
	registry.Register(viewingTool{name: "read_something"})
	registry.Register(secondViewingTool{})
	for _, tc := range []struct {
		name string
		held func(string) bool
	}{
		{"no view provider wired at all", nil},
		{"a provider holding nothing", func(string) bool { return false }},
		{"a provider holding one of two", func(uri string) bool { return uri == viewURI }},
		{"a provider holding both", func(string) bool { return true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
			d.viewHeld = tc.held
			listed := d.toolList(agentHolding(principal.ScopeRead), framing{modern: true, apps: true})
			if len(listed) != 2 {
				t.Fatalf("tools/list returned %d entries; a missing view must never withdraw a tool", len(listed))
			}
			for _, tool := range listed {
				meta, carried := tool[fieldMeta]
				if !carried {
					continue
				}
				members, shaped := meta.(map[string]any)
				if !shaped {
					t.Fatalf("%v carries a _meta that is not an object: %#v", tool[fieldName], meta)
				}
				ui, declared := members[metaUIKey].(*toolUIWire)
				if !declared {
					t.Fatalf("%v carries a _meta.ui that is not a view declaration: %#v", tool[fieldName], members[metaUIKey])
				}
				named := ui.ResourceURI
				if tc.held == nil || !tc.held(named) {
					t.Errorf("%v names the view %q, which this server is not serving", tool[fieldName], named)
				}
			}
		})
	}
}

// The extension is advertised on the strength of a document that ARRIVED, not on
// a declaration. A host that saw it advertised is entitled to expect a view to
// prefetch, so a deployment whose views all failed must look exactly like one
// that has none.
func TestTheExtensionIsNotAdvertisedWhenNoViewIsHeld(t *testing.T) {
	registry := NewRegistry(nil, nil)
	registry.Register(viewingTool{name: "read_something"})
	d := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
	d.viewHeld = func(string) bool { return false }
	if d.appsServed() {
		t.Error("a server holding no view still advertises the App extension, so a host will prefetch a 404")
	}
}

// rpcRendering is one request from a client that declared it can render a
// view. The App members — the documents themselves and their policies — exist
// for this client and no other.
func rpcRendering(t *testing.T, d *Dispatcher, method, params string) rpcResponse {
	t.Helper()
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return d.handle(agentHolding(principal.ScopeRead), req, framing{modern: true, apps: true})
}

// A client that did not declare the App extension is served the resource
// surface exactly as it was before any view existed — which is the promise
// apps.go's header makes, and which the tool listing kept while the document
// catalogue did not.
//
// The two halves are asserted TOGETHER because the failure that matters is the
// one where they disagree: a catalogue that hid a view while the read still
// served it would let a client learn a document exists by asking for a URI it
// was never shown.
func TestAClientThatCannotRenderAViewIsNeitherShownOneNorServedOne(t *testing.T) {
	d := dispatcherServingAView(t)

	listed, err := json.Marshal(rpc(t, d, "resources/list", "").Result)
	if err != nil {
		t.Fatalf("encoding resources/list: %v", err)
	}
	if strings.Contains(string(listed), viewURI) {
		t.Errorf("the legacy era is advertised a view it has no way to declare for:\n%s", listed)
	}

	read := rpc(t, d, "resources/read", `{"uri":"`+viewURI+`"}`)
	if read.Error == nil {
		t.Fatalf("the legacy era read a view document: %v", read.Result)
	}
	if read.Error.Code != resourceNotFound {
		t.Errorf("reading a withheld view answered %d, want %d — the same answer an "+
			"unknown URI gets, or the refusal itself reports the document exists",
			read.Error.Code, resourceNotFound)
	}
}

// And an ORDINARY document is unaffected by the same filter. The gate is about
// what a client can render, not about narrowing the catalogue in general — a
// build that filtered everything would pass the assertions above for the wrong
// reason.
func TestAnOrdinaryDocumentIsStillServedToAClientWithNoViewSupport(t *testing.T) {
	d := dispatcherServingAView(t).WithResources(stubResources{
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

	listed, err := json.Marshal(rpc(t, d, "resources/list", "").Result)
	if err != nil {
		t.Fatalf("encoding resources/list: %v", err)
	}
	if !strings.Contains(string(listed), "margince://schema/query") {
		t.Errorf("an ordinary document was withheld from a client with no view support:\n%s", listed)
	}
	if read := rpc(t, d, "resources/read", `{"uri":"margince://schema/query"}`); read.Error != nil {
		t.Errorf("an ordinary document could not be read without declaring the App extension: %v", read.Error)
	}
}
