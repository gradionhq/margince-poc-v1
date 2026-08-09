// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The App extension on the wire: how a tool advertises its view, how a view
// declares what it may reach, and the ONE place either is rendered.
//
// This is SEP-1865 (extension revision 2026-01-26), which is not part of the
// core protocol revision the transport negotiates — a host opts into it per
// request, and one that does not is served the surface exactly as it was
// before any view existed.
//
// WHY THE RENDERING IS HERE AND NOT AT EACH SURFACE. `_meta.ui` reaches a
// client from two places — a tool in tools/list, a document in
// resources/list and resources/read — and the two halves are one promise: the
// tool names a view, the view states its limits. Rendered where they are
// served, they would be two spellings of one contract, and the failure that
// matters is precisely the one where they disagree.
//
// NOTHING HERE CARRIES AUTHORITY. A view is a second renderer for an answer a
// tool already gives in text. It holds no credential, spends no scope, and
// cannot reach a record its tool would not have answered — which is why none
// of this appears anywhere near the admission gate.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// extensionUI is the App extension's identifier, used in the three places that
// must agree: what a client declares in its per-request capabilities, what
// server/discover advertises, and which requests are served `_meta.ui`. One
// spelling, for the reason extensionTasks gives.
const extensionUI = "io.modelcontextprotocol/ui"

// metaUIKey is the member both halves hang under, inside the `_meta` object the
// protocol reserves for extensions.
const metaUIKey = "ui"

// declaresUI reports whether this request's capabilities declare the App
// extension, and fails closed for every reason declaresExtension gives.
func declaresUI(capabilities json.RawMessage) bool {
	return declaresExtension(capabilities, extensionUI)
}

// assertViewDeclaration holds a tool's own view declaration to the three things
// that can be judged from ONE spec, at the one door every tool comes through.
//
// What it deliberately does NOT check is whether the named document exists. That
// is a cross-reference between two things injected independently — the registry
// and a resource provider — and neither knows about the other here. The composed
// surface answers it instead (compose's App sweep), where both halves are known
// and the failure is a build that does not go green rather than a process that
// does not start.
func assertViewDeclaration(spec mcp.ToolSpec) error {
	if spec.UI == nil {
		return nil
	}
	// A view with no URI, or one outside the scheme, names nothing a host will
	// fetch. A host dispatches on `ui://` to tell a view from a document, so a
	// well-formed URI in any other scheme is not a near miss — it is a tool that
	// advertises a view no host will ever render, silently.
	if !strings.HasPrefix(spec.UI.ResourceURI, mcp.AppURIScheme) {
		return fmt.Errorf("%s declares the view %q, which is not a %s URI — a host tells a view from an ordinary document by that scheme, "+
			"so this tool would advertise a view nothing renders", spec.Name, spec.UI.ResourceURI, mcp.AppURIScheme)
	}
	modelReaches := len(spec.UI.Visibility) == 0
	for _, audience := range spec.UI.Visibility {
		switch audience {
		case mcp.VisibilityModel:
			modelReaches = true
		case mcp.VisibilityApp:
		default:
			// An audience no host recognises does not widen the list, it
			// narrows the tool: a host reads the entries it knows and this one
			// is not among them, so the reach the author meant is not the reach
			// the tool gets.
			return fmt.Errorf("%s declares the audience %q, which is not %q or %q — a host reads only the audiences it knows, "+
				"so an unrecognised one narrows this tool rather than widening it",
				spec.Name, audience, mcp.VisibilityModel, mcp.VisibilityApp)
		}
	}
	// A tool the model cannot select is a capability that exists only inside a
	// rendered view. A host MUST leave such a tool out of the agent's catalog,
	// so every client that does not render — every script, every CI check, every
	// host without the extension — loses the capability entirely. This surface
	// serves views as a second renderer for an answer a tool already gives, and
	// that promise is exactly this check.
	if !modelReaches {
		return fmt.Errorf("%s is declared visible to %q alone, which makes it a capability only a rendered view can reach — "+
			"a view is a second renderer for an answer this tool already gives, never its only door",
			spec.Name, mcp.VisibilityApp)
	}
	return nil
}

// appsServed reports whether this server has any view to offer, DERIVED from
// the registry rather than from a flag someone sets.
//
// It answers the capability claim, and a claim has to be true of the surface
// that is actually assembled: a deployment whose tools carry no view must not
// advertise the extension, because a host that saw it advertised is entitled to
// expect a document to prefetch. Reading the registry means a unit added or
// withdrawn moves this answer in the same commit, with nothing to keep current.
//
// It does NOT check that the resource provider publishes each URI, and neither
// does Register: that cross-reference is held by the composed-surface sweep in
// compose, where the registry and the providers are both known. Asking it here
// would mean calling a per-caller Resources(ctx) with no caller, which answers a
// different question than the one being asked.
func (s *Dispatcher) appsServed() bool {
	for _, spec := range s.registry.Specs() {
		if spec.UI != nil {
			return true
		}
	}
	return false
}

// appsOffered reports whether THIS request is served the App extension's
// members: the request declared it, and this server has views to declare.
//
// Both halves, because each alone produces a different broken promise. Without
// the declaration a client is handed a member it never said it could render;
// without a view the server advertises a capability it cannot honour.
func (s *Dispatcher) appsOffered(fr framing) bool {
	return fr.apps && s.appsServed()
}

// toolUIWire is one tool's `_meta.ui` as a client reads it.
type toolUIWire struct {
	//nolint:tagliatelle // resourceUri is the extension's wire member, camelCase by the specification
	ResourceURI string   `json:"resourceUri"`
	Visibility  []string `json:"visibility"`
}

// toolUIMeta renders a tool's view declaration, and answers nil for a tool
// that has none — which is most of them, and which is why the absence is a
// return value rather than an empty object. A `_meta` member on every tool
// would be catalog bytes spent, on every client, for the tools that have no
// view.
func toolUIMeta(spec mcp.ToolSpec) *toolUIWire {
	if spec.UI == nil {
		return nil
	}
	return &toolUIWire{
		ResourceURI: spec.UI.ResourceURI,
		Visibility:  visibilityOrBoth(spec.UI.Visibility),
	}
}

// visibilityOrBoth resolves an undeclared audience list to BOTH audiences.
//
// The default cannot be the zero value passed through, because an empty list is
// the protocol's own spelling for "no audience": a host MUST leave a tool out
// of the model's catalog when `visibility` excludes "model", so serving `[]`
// would withdraw from the model a tool that was model-callable before it grew a
// view. Saying nothing about audience has to mean the audience did not change.
func visibilityOrBoth(declared []string) []string {
	if len(declared) > 0 {
		return declared
	}
	return []string{mcp.VisibilityModel, mcp.VisibilityApp}
}

// resourceUIWire is one view's `_meta.ui` as a host reads it before it builds
// the sandbox.
type resourceUIWire struct {
	CSP         resourceCSPWire         `json:"csp"`
	Permissions resourcePermissionsWire `json:"permissions"`
	Domain      string                  `json:"domain,omitempty"`
	//nolint:tagliatelle // prefersBorder is the extension's wire member, camelCase by the specification
	PrefersBorder bool `json:"prefersBorder,omitempty"`
}

// resourceCSPWire carries the four allowlists. NONE of them is `omitempty`, and
// every one is normalized to an empty array rather than left nil — see
// originsOrEmpty.
type resourceCSPWire struct {
	//nolint:tagliatelle // the four domain members are the extension's own, camelCase by the specification
	ConnectDomains []string `json:"connectDomains"`
	//nolint:tagliatelle // as above
	ResourceDomains []string `json:"resourceDomains"`
	//nolint:tagliatelle // as above
	FrameDomains []string `json:"frameDomains"`
	//nolint:tagliatelle // as above
	BaseURIDomains []string `json:"baseUriDomains"`
}

type resourcePermissionsWire struct {
	Camera      bool `json:"camera"`
	Microphone  bool `json:"microphone"`
	Geolocation bool `json:"geolocation"`
	//nolint:tagliatelle // clipboardWrite is the extension's wire member, camelCase by the specification
	ClipboardWrite bool `json:"clipboardWrite"`
}

// resourceUIMeta renders a view's own declaration, and answers nil for an
// ordinary document — a sandbox policy on something no host will sandbox is a
// claim about nothing.
func resourceUIMeta(resource mcp.Resource) *resourceUIWire {
	if resource.UI == nil {
		return nil
	}
	return &resourceUIWire{
		CSP: resourceCSPWire{
			ConnectDomains:  originsOrEmpty(resource.UI.CSP.ConnectDomains),
			ResourceDomains: originsOrEmpty(resource.UI.CSP.ResourceDomains),
			FrameDomains:    originsOrEmpty(resource.UI.CSP.FrameDomains),
			BaseURIDomains:  originsOrEmpty(resource.UI.CSP.BaseURIDomains),
		},
		Permissions: resourcePermissionsWire{
			Camera:         resource.UI.Permissions.Camera,
			Microphone:     resource.UI.Permissions.Microphone,
			Geolocation:    resource.UI.Permissions.Geolocation,
			ClipboardWrite: resource.UI.Permissions.ClipboardWrite,
		},
		Domain:        resource.UI.Domain,
		PrefersBorder: resource.UI.PrefersBorder,
	}
}

// originsOrEmpty normalizes an allowlist so the wire carries `[]` and never
// `null`.
//
// This is the self-contained posture's whole load-bearing detail. A host builds
// its content-security policy from these lists and MUST NOT admit an origin
// they do not name — so an empty list is an instruction to deny everything,
// while `null` is a list the host was not given and may read as "unspecified",
// which is where a permissive default lives. The two look alike in Go and mean
// opposite things to the thing enforcing them.
func originsOrEmpty(origins []string) []string {
	if origins == nil {
		return []string{}
	}
	return origins
}
