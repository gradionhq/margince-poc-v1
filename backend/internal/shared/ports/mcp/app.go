// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mcp

// The App half of the surface: a tool may name an interactive view, and the
// view declares what it is allowed to reach.
//
// This is the MCP Apps extension (SEP-1865, extension revision 2026-01-26),
// which is NOT part of the core protocol revision the transport negotiates —
// a host opts into it per request, and a host that does not is served a
// surface with none of this on it.
//
// WHY THE TWO HALVES LIVE IN ONE FILE. The tool names a view; the view states
// its own limits. Neither is readable without the other, and the one mistake
// that matters spans both: a tool that names a view nobody serves, or a view
// whose limits are wider than the one thing it renders. Keeping them apart
// would put the two ends of every such defect in two files.
//
// WHY THE VIEW DECLARES ITS LIMITS AND NOT THE TOOL. A view is a document with
// an origin, a script and a network reach; a tool is a call. The host builds
// its sandbox from the document's declaration before any call is made — it can
// prefetch and security-review a view without invoking anything — so the
// declaration has to travel with the document.

// ToolUI is what a tool says about the view that renders its result. Nil on
// every tool that has none, which is most of them: a tool without a view is
// the ordinary case and answers text as it always did.
type ToolUI struct {
	// ResourceURI names the view, and MUST be a URI the server's own resource
	// provider publishes. The protocol makes that the server's obligation
	// rather than the host's, because a host is entitled to prefetch the view
	// before the tool is ever called — so a URI nobody serves is not a broken
	// render, it is a host fetching a 404 and an App that silently never
	// appears.
	//
	// WHERE THAT IS ENFORCED is not registration, which sees one spec and no
	// provider: it is a sweep over the COMPOSED surface, where the registry and
	// the wired providers are both known. Registration refuses only what one
	// spec can be judged on — an empty URI, one outside the scheme, an
	// unrecognised audience.
	ResourceURI string
	// Visibility is who may reach this tool: "model", "app", or both. A tool
	// the model cannot see is one a host MUST leave out of the agent's
	// catalog, which makes ["app"] alone a capability that exists only inside
	// a rendered view — and a surface with one of those has a capability no
	// unrendered client can reach.
	//
	// Empty means both, which is the honest default for a surface whose tools
	// were all model-callable before they had views.
	Visibility []string
}

// The two audiences a tool may be reachable by, spelled as constants because
// the strings cross the wire and a typo in one would silently narrow a tool's
// reach to nothing a host recognises.
const (
	// VisibilityModel — the agent may select and call it.
	VisibilityModel = "model"
	// VisibilityApp — a rendered view may call it.
	VisibilityApp = "app"
)

// ResourceUI is what a view declares about itself: the origins it may reach
// and the browser capabilities it asks for. Nil on an ordinary document,
// which is every resource that is not a view.
//
// EVERYTHING HERE IS AN ALLOWLIST, and an empty one denies. The host builds
// the sandbox's content-security policy from these lists and MUST NOT admit
// an origin they do not name, so a view that declares nothing can reach
// nothing — no fetch, no remote script, no external image. That is the
// posture a self-contained view wants, and it is the reason a view here
// declares its lists explicitly rather than inheriting a default: an
// allowlist that grows has to grow in a diff, next to the reason.
type ResourceUI struct {
	CSP         ResourceCSP
	Permissions ResourcePermissions
	// Domain optionally asks the host for a dedicated sandbox origin. It is
	// host-specific and only means anything to a view that needs an origin of
	// its own to persist against; a stateless view leaves it empty and is
	// sandboxed in whatever opaque origin the host chooses.
	Domain string
	// PrefersBorder asks the host to draw a visible edge around the view. It
	// is presentation, carries no authority, and a host may ignore it.
	PrefersBorder bool
}

// ResourceCSP is the origin allowlist, one list per thing a document can
// reach. Each is nil on a view that reaches nothing of that kind, and nil is
// a denial rather than an omission — see ResourceUI.
type ResourceCSP struct {
	// ConnectDomains: fetch, XHR, WebSocket.
	ConnectDomains []string
	// ResourceDomains: images, scripts, stylesheets, fonts.
	ResourceDomains []string
	// FrameDomains: nested frames.
	FrameDomains []string
	// BaseURIDomains: what the document may set as its base URI — which
	// decides where every relative URL in it resolves to.
	BaseURIDomains []string
}

// Empty reports whether this policy admits no origin at all.
//
// It exists so the gate that holds a view to its own claims can ask the
// question in one place: a view asserting it is self-contained is asserting
// exactly this, and a reviewer reading a diff that adds an origin should find
// it beside the reason it was added.
func (c ResourceCSP) Empty() bool {
	return len(c.ConnectDomains) == 0 && len(c.ResourceDomains) == 0 &&
		len(c.FrameDomains) == 0 && len(c.BaseURIDomains) == 0
}

// ResourcePermissions are the browser capabilities a view asks the host to
// grant its sandbox. Every one is false on a view that needs none.
//
// ON THE WIRE THIS IS A SET, NOT A SET OF FLAGS. The extension declares each
// permission as an optional object member (`camera?: {}`), so a host reads the
// member's PRESENCE as the request — which means a shape that spelled every
// permission out as `false` would present four REQUESTED permissions to a host
// reading presence, and a view asking for nothing would get the widest sandbox
// available. The booleans here are for the Go caller; the renderer emits only
// the ones asked for, and omits the member entirely when there are none.
type ResourcePermissions struct {
	Camera         bool
	Microphone     bool
	Geolocation    bool
	ClipboardWrite bool
}

// AppMIMEType is the content type a view MUST be served as. The profile
// parameter is not decoration: a host dispatches on it to tell an interactive
// view from an ordinary HTML document, so a view served as plain text/html is
// rendered as a document and never becomes an App.
const AppMIMEType = "text/html;profile=mcp-app"

// AppURIScheme is the prefix a view's URI MUST carry. It is what separates a
// view from every other document a provider publishes, on a surface where
// both travel through one resources/list.
const AppURIScheme = "ui://"
