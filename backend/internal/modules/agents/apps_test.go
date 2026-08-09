// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The App extension from the negotiation side: what a request has to declare
// before a view is offered to it, and what a request that declared nothing is
// served.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The identifiers cross the wire, so they are asserted against the
// specification's own spelling rather than against themselves. A typo here
// reads every client as unable to render a view — which looks exactly like a
// client that chose not to.
func TestTheAppExtensionUsesTheSpecificationsOwnTokens(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{extensionUI, "io.modelcontextprotocol/ui"},
		{mcp.AppMIMEType, "text/html;profile=mcp-app"},
		{mcp.AppURIScheme, "ui://"},
		{mcp.VisibilityModel, "model"},
		{mcp.VisibilityApp, "app"},
		{metaUIKey, "ui"},
	} {
		if tc.got != tc.want {
			t.Errorf("token = %q, want the extension's own spelling %q", tc.got, tc.want)
		}
	}
}

// One negotiation mechanism, two extensions. declaresExtension replaced
// declaresTasks's hand-spelled key so the second extension is not bolted onto
// the first's spelling — which means the exactness the Tasks tests establish
// has to still hold when the key is a parameter.
func TestOnlyAnExactlySpelledAppDeclarationCounts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities string
		want         bool
	}{
		{"the specification's own spelling", `{"extensions":{"io.modelcontextprotocol/ui":{}}}`, true},
		{"declared beside Tasks", `{"extensions":{"io.modelcontextprotocol/tasks":{},"io.modelcontextprotocol/ui":{}}}`, true},
		{"a mis-cased member", `{"Extensions":{"io.modelcontextprotocol/ui":{}}}`, false},
		{"a mis-cased extension name", `{"extensions":{"IO.MODELCONTEXTPROTOCOL/UI":{}}}`, false},
		{"only Tasks declared", `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`, false},
		{"no extensions at all", `{}`, false},
		{"a null extensions member", `{"extensions":null}`, false},
		{"the extension declared as null", `{"extensions":{"io.modelcontextprotocol/ui":null}}`, false},
		{"the extension declared as true", `{"extensions":{"io.modelcontextprotocol/ui":true}}`, false},
		{"capabilities that do not decode", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaresUI(json.RawMessage(tc.capabilities)); got != tc.want {
				t.Errorf("declaresUI(%s) = %v, want %v", tc.capabilities, got, tc.want)
			}
		})
	}
}

// Generalising the negotiation must not have changed what Tasks reads. The two
// extensions share one mechanism now, and a bug in the shared reader would
// otherwise show up as a Tasks regression nothing in this file is watching.
func TestTheSharedNegotiationStillReadsTasksExactly(t *testing.T) {
	for _, tc := range []struct {
		capabilities string
		want         bool
	}{
		{`{"extensions":{"io.modelcontextprotocol/tasks":{}}}`, true},
		{`{"extensions":{"io.modelcontextprotocol/ui":{}}}`, false},
		{`{"Extensions":{"io.modelcontextprotocol/tasks":{}}}`, false},
	} {
		if got := declaresTasks(json.RawMessage(tc.capabilities)); got != tc.want {
			t.Errorf("declaresTasks(%s) = %v, want %v", tc.capabilities, got, tc.want)
		}
	}
}

// A tool's view reaches the wire as `_meta.ui`, and a tool without one puts
// nothing there. The absence matters as much as the presence: a `_meta` member
// on every tool would grow the catalog every client reads for the sake of the
// tools that have no view.
func TestOnlyAToolWithAViewCarriesUIMeta(t *testing.T) {
	withView := mcp.ToolSpec{Name: "read_brief", UI: &mcp.ToolUI{
		ResourceURI: "ui://margince/account-brief.html",
	}}
	meta := toolUIMeta(withView)
	if meta == nil {
		t.Fatal("a tool naming a view carries no _meta.ui, so no host can find its view")
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("encoding the tool's _meta.ui: %v", err)
	}
	// Both members are asserted on the BYTES rather than on the struct: what a
	// host reads is the JSON, and a mis-tagged field would satisfy a struct
	// comparison while advertising nothing.
	const want = `{"resourceUri":"ui://margince/account-brief.html","visibility":["model","app"]}`
	if string(encoded) != want {
		t.Errorf("_meta.ui = %s, want %s", encoded, want)
	}
	if toolUIMeta(mcp.ToolSpec{Name: "read_record"}) != nil {
		t.Error("a tool with no view carries a _meta.ui anyway, which every client would read and no host could use")
	}
}

// An empty Visibility means BOTH audiences, and it has to reach the wire that
// way rather than as an empty list. An empty list is the protocol's spelling
// for "no audience", so serving one would withdraw a tool from the model that
// was model-callable before it grew a view.
func TestAnUndeclaredVisibilityReachesTheWireAsBothAudiences(t *testing.T) {
	meta := toolUIMeta(mcp.ToolSpec{Name: "who_knows", UI: &mcp.ToolUI{ResourceURI: "ui://margince/x.html"}})
	if meta == nil {
		t.Fatal("no _meta.ui at all")
	}
	if got := meta.Visibility; len(got) != 2 || got[0] != mcp.VisibilityModel || got[1] != mcp.VisibilityApp {
		t.Errorf("visibility = %v, want both audiences — an empty list would withdraw the tool from the model", got)
	}
}

// A declared Visibility is passed through as declared, so a tool that really
// does mean one audience gets it. Without this the default above would be
// indistinguishable from an override that silently does not work.
func TestADeclaredVisibilityIsServedAsDeclared(t *testing.T) {
	meta := toolUIMeta(mcp.ToolSpec{Name: "who_knows", UI: &mcp.ToolUI{
		ResourceURI: "ui://margince/x.html",
		Visibility:  []string{mcp.VisibilityModel},
	}})
	if meta == nil {
		t.Fatal("no _meta.ui at all")
	}
	if got := meta.Visibility; len(got) != 1 || got[0] != mcp.VisibilityModel {
		t.Errorf("visibility = %v, want the declared [model] alone", got)
	}
}

// A view's own declaration reaches the wire as the extension spells it, and an
// EMPTY allowlist has to survive the trip. This is the assertion the
// self-contained posture rests on: a host builds its sandbox from these lists
// and admits no origin they do not name, so a list that vanished into `null`
// or was dropped as a zero value would hand the host nothing to deny from.
func TestAViewsEmptyAllowlistReachesTheWireAsAnEmptyAllowlist(t *testing.T) {
	meta := resourceUIMeta(mcp.Resource{
		URI: "ui://margince/account-brief.html", UI: &mcp.ResourceUI{PrefersBorder: true},
	})
	if meta == nil {
		t.Fatal("a view carries no _meta.ui, so the host has no policy to build a sandbox from")
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("encoding the view's _meta.ui: %v", err)
	}
	const want = `{"csp":{"connectDomains":[],"resourceDomains":[],"frameDomains":[],"baseUriDomains":[]},` +
		`"permissions":{"camera":false,"microphone":false,"geolocation":false,"clipboardWrite":false},` +
		`"prefersBorder":true}`
	if string(encoded) != want {
		t.Errorf("_meta.ui = %s,\nwant                %s", encoded, want)
	}
	if resourceUIMeta(mcp.Resource{URI: "margince://schema/query"}) != nil {
		t.Error("an ordinary document carries a view's _meta.ui, which claims a sandbox policy for something no host will sandbox")
	}
}

// A declared origin is served as declared. Without this the empty-list
// assertion above would also pass against a renderer that drops every list.
func TestADeclaredOriginReachesTheWire(t *testing.T) {
	meta := resourceUIMeta(mcp.Resource{URI: "ui://margince/x.html", UI: &mcp.ResourceUI{
		CSP:         mcp.ResourceCSP{ConnectDomains: []string{"https://example.test"}},
		Permissions: mcp.ResourcePermissions{ClipboardWrite: true},
		Domain:      "app.example.test",
	}})
	if meta == nil {
		t.Fatal("no _meta.ui at all")
	}
	if got := meta.CSP.ConnectDomains; len(got) != 1 || got[0] != "https://example.test" {
		t.Errorf("connectDomains = %v, want the declared origin", got)
	}
	if !meta.Permissions.ClipboardWrite {
		t.Error("a declared clipboard permission did not reach the wire")
	}
	if meta.Domain != "app.example.test" {
		t.Errorf("domain = %q, want the declared sandbox origin", meta.Domain)
	}
}

// The defects a tool's OWN view declaration can carry, refused at the one door
// every tool comes through. Each is a property of a single spec, which is why it
// belongs here rather than in the composed-surface sweep: the cross-reference to
// a document another injection publishes is a different question, asked in
// compose where both halves are known.
//
// Every one is proved against a spec that breaks it. A registration gate only
// ever run over a clean tree is a gate nobody has seen fail.
func TestRegisterRefusesAViewDeclarationThatCannotWork(t *testing.T) {
	viewing := func(ui *mcp.ToolUI) *fakeTool {
		return &fakeTool{spec: mcp.ToolSpec{
			Name: "read_something", Title: "Read something", Version: testToolVersion,
			Description: describedForRegistration, Tier: mcp.TierAutoExecute,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			UI:          ui,
		}}
	}
	for _, tc := range []struct {
		why string
		ui  *mcp.ToolUI
	}{
		{
			why: "a view with no URI names nothing for the host to fetch",
			ui:  &mcp.ToolUI{},
		},
		{
			why: "a URI outside the ui:// scheme is not a view, and a host dispatching on the scheme would never render it",
			ui:  &mcp.ToolUI{ResourceURI: "margince://schema/query"},
		},
		{
			why: "an audience outside the closed set is one no host recognises, and an unrecognised entry narrows the tool to nothing",
			ui:  &mcp.ToolUI{ResourceURI: viewURI, Visibility: []string{"model", "everyone"}},
		},
		{
			why: "a tool the model cannot select is a capability that exists only inside a rendered view",
			ui:  &mcp.ToolUI{ResourceURI: viewURI, Visibility: []string{mcp.VisibilityApp}},
		},
	} {
		t.Run(tc.why, func(t *testing.T) {
			mustPanic(t, tc.why, func() { NewRegistry(nil, nil).Register(viewing(tc.ui)) })
		})
	}
}

// And the shape that is fine registers, so the gate above is not simply
// refusing every view. Without this the four refusals could all be one
// over-broad check nobody would notice.
func TestRegisterAcceptsAWellFormedViewDeclaration(t *testing.T) {
	for _, ui := range []*mcp.ToolUI{
		{ResourceURI: viewURI},
		{ResourceURI: viewURI, Visibility: []string{mcp.VisibilityModel}},
		{ResourceURI: viewURI, Visibility: []string{mcp.VisibilityModel, mcp.VisibilityApp}},
	} {
		r := NewRegistry(nil, nil)
		r.Register(&fakeTool{spec: mcp.ToolSpec{
			Name: "read_something", Title: "Read something", Version: testToolVersion,
			Description: describedForRegistration, Tier: mcp.TierAutoExecute,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			UI:          ui,
		}})
		if _, registered := r.Spec("read_something"); !registered {
			t.Errorf("a well-formed view declaration %+v was not registered", ui)
		}
	}
}
