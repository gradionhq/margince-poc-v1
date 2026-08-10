// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The App extension held against the COMPOSED surface, which is the only place
// its two halves meet: modules/agents registers tools that name views, an
// injected provider publishes the documents, and neither knows the other exists.
// Register can refuse a malformed declaration from one spec; only here can
// anything ask whether the document a tool names is actually served.
//
// Every sweep derives its subject from the assembled surface rather than from a
// list of views someone maintains, so a view added, renamed or withdrawn reaches
// these assertions in the commit that changes the product.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// viewDocument is a document shaped the way the build emits one: a doctype, the
// licence header, the catalogue title, and everything else inline.
//
// IT IS A FIXTURE AND NOT THE ARTIFACT, and this file says so rather than
// letting a reader believe otherwise. `make check` has no network and no built
// frontend, so nothing here can read the bytes a host will actually render. The
// real bytes are owned by two lanes that CAN see them: the build-time parsed
// validator in frontend/scripts/vite-inline-views.ts, which refuses to emit a
// document that reaches off-origin, and the Playwright zero-request run. What
// these sweeps prove is the WIRING — that a tool's declaration and the document
// the server is holding are read from one value.
func viewDocument(title string) string {
	return `<!doctype html>
<!--
SPDX-License-Identifier: BUSL-1.1
SPDX-FileCopyrightText: 2026 Gradion
-->
<html lang="en"><head><meta charset="utf-8"><title>` + title + `</title>
<style>.row{border:1px solid var(--borderSubtle)}</style></head>
<body><main id="root"></main><script>
function render(root, data) { root.replaceChildren(); }
</script></body></html>`
}

// primedViews is the REAL view provider, primed through the REAL fetcher and the
// REAL admission check against a stand-in web tier.
//
// The web tier is the one boundary a test may replace; everything between it and
// the resource seam is production code. A hand-built provider holding
// hand-inserted documents would prove nothing about the path a deployment takes
// — it is the shape review-loop rule 6 exists to refuse.
//
// titles maps each view's URI to the title its document will declare; a URI left
// out is one the origin does not serve, which is how a test drives partial
// availability.
func primedViews(t *testing.T, titles map[string]string) *apps.Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		title, serving := titles["ui://margince"+strings.TrimPrefix(r.URL.Path, "/mcp-apps")]
		if !serving {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := io.WriteString(w, viewDocument(title)); err != nil {
			t.Errorf("serving the stand-in view: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the stand-in origin: %v", err)
	}
	views := apps.NewProvider(apps.NewFetcher(base), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := views.Prime(t.Context()); err != nil {
		t.Fatalf("priming the view provider: %v", err)
	}
	return views
}

// bothViews is the titles map for a deployment serving everything it declares.
func bothViews() map[string]string {
	return map[string]string{
		apps.AccountBriefURI:    "Morning brief",
		apps.RelationshipMapURI: "Who knows this contact",
	}
}

// composedResources is the view half of the resource surface, assembled through
// the same constructor the transport uses.
//
// IT IS NOT THE WHOLE PRODUCTION FAN-OUT, and the difference matters enough to
// state: mcpHandler composes the query vocabulary FIRST and the views second,
// and the vocabulary needs a pool that these sweeps have no business opening. So
// composeResources drops the nil and returns the view provider alone — the same
// conditional wiring an installation without a vocabulary gets, and the same
// path every assertion below is about.
//
// What that leaves unmeasured is a URI collision BETWEEN the production
// providers. TestTheProductionProvidersPublishDisjointSchemes covers it from the
// other side, structurally, because it can be answered without a pool — and it
// reads mcpResourceProviders, so a provider added to the transport enters it
// automatically rather than being a list somebody has to remember.
func composedResources(t *testing.T) mcp.ResourceProvider {
	t.Helper()
	return composeResources(mcpResourceProviders(nil, primedViews(t, bothViews()))...)
}

// readerCtx is an agent holding `read`, which is the scope a view requires. The
// resource surface is scope-filtered for agents exactly as the tool list is, so
// a sweep on an actor-less context would be reading an empty catalogue and
// passing for the wrong reason.
func readerCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:apps", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
}

// The protocol's own server obligation: a `ui://` URI a tool names MUST exist on
// the server. A host is entitled to prefetch a view before the tool is ever
// called, so a URI nobody publishes is not a render that fails — it is a host
// fetching a 404 and a panel that silently never appears.
func TestEveryToolsViewIsAServedDocument(t *testing.T) {
	published := map[string]mcp.Resource{}
	for _, r := range composedResources(t).Resources(readerCtx()) {
		published[r.URI] = r
	}
	named := 0
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if spec.UI == nil {
			continue
		}
		named++
		view, served := published[spec.UI.ResourceURI]
		if !served {
			t.Errorf("%s names the view %q, which no wired provider publishes — a host that prefetches it "+
				"gets a not-found and renders nothing, with no error anywhere the operator can see",
				spec.Name, spec.UI.ResourceURI)
			continue
		}
		// And the document has to be readable, not merely advertised. A
		// descriptor with no content behind it fails at exactly the same point
		// as a missing descriptor, one step later.
		if _, err := composedResources(t).ReadResource(readerCtx(), spec.UI.ResourceURI); err != nil {
			t.Errorf("%s names the view %q, which is advertised but cannot be read: %v", spec.Name, view.URI, err)
		}
	}
	if named == 0 {
		t.Fatal("no registered tool names a view, so this sweep proved nothing — either the views were withdrawn " +
			"or the tools stopped declaring them")
	}
}

// A view has to be served under the profile a host dispatches on. Plain
// `text/html` is a document, and a host reading it that way renders it as one
// rather than as an interactive panel — which looks like a styling bug, not a
// content-type bug.
func TestEveryViewIsServedUnderTheAppProfile(t *testing.T) {
	views := 0
	for _, r := range composedResources(t).Resources(readerCtx()) {
		if !strings.HasPrefix(r.URI, mcp.AppURIScheme) {
			continue
		}
		views++
		if r.MIMEType != mcp.AppMIMEType {
			t.Errorf("the view %s is advertised as %q, want %q — a host tells an interactive view from an "+
				"ordinary document by that profile", r.URI, r.MIMEType, mcp.AppMIMEType)
		}
		if r.UI == nil {
			t.Errorf("the view %s declares no sandbox policy, so the host has nothing to build a "+
				"content-security policy from", r.URI)
			continue
		}
		// The self-contained claim, asserted rather than assumed. The day a view
		// needs an origin this fails, and the origin arrives in a diff beside
		// the reason for it — which is the whole point of stating the claim here.
		if !r.UI.CSP.Empty() {
			t.Errorf("the view %s declares reachable origins %+v. That may be correct, but it is a widening of "+
				"the sandbox and this assertion is where it gets argued: state why in the diff that adds it",
				r.URI, r.UI.CSP)
		}
		// Compared against the ZERO value rather than field by field: a permission
		// added to the seam later would be invisible to a hand-written list, and
		// silently-unchecked is the one thing a permission must not be.
		if r.UI.Permissions != (mcp.ResourcePermissions{}) {
			t.Errorf("the view %s asks for browser permissions %+v; none of these views needs one",
				r.URI, r.UI.Permissions)
		}
	}
	if views == 0 {
		t.Fatal("the composed surface publishes no view, so this sweep proved nothing")
	}
}

// The inverse of the URI sweep: a document published under `ui://` that no tool
// names is a view no host will ever be told to render. Harmless, and exactly the
// shape a half-finished or half-removed App leaves behind.
func TestEveryServedViewIsNamedByATool(t *testing.T) {
	named := map[string]string{}
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if spec.UI != nil {
			named[spec.UI.ResourceURI] = spec.Name
		}
	}
	for _, r := range composedResources(t).Resources(readerCtx()) {
		if !strings.HasPrefix(r.URI, mcp.AppURIScheme) {
			continue
		}
		if _, claimed := named[r.URI]; !claimed {
			t.Errorf("the view %s is published but no tool names it, so no host is ever told to render it — "+
				"either a tool lost its declaration or this document outlived the tool it was written for", r.URI)
		}
	}
}

// D3's exit criterion, as a gate: a tool with a view still answers without one.
// Every client that does not render — a script, a CI check, a host without the
// extension — has to get the same capability, so the view can never be the only
// way to reach an answer.
//
// Proven through the SCOPE model rather than by calling handlers: a tool the
// model may select is a tool any client can call and read the text of. A
// view-only tool is precisely one the model is not offered, and Register already
// refuses that — this sweep is what proves the refusal covers the real surface
// rather than only the specs a unit test invented.
func TestNoCapabilityLivesOnlyInsideAView(t *testing.T) {
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if spec.UI == nil {
			continue
		}
		modelReaches := len(spec.UI.Visibility) == 0
		for _, audience := range spec.UI.Visibility {
			if audience == mcp.VisibilityModel {
				modelReaches = true
			}
		}
		if !modelReaches {
			t.Errorf("%s is reachable only from a rendered view, so every client that does not render loses "+
				"the capability entirely", spec.Name)
		}
		if spec.OutputSchema == nil {
			t.Errorf("%s carries a view but declares no result shape, so the answer a non-rendering client "+
				"reads is undescribed — the view would be the only documented way to understand it", spec.Name)
		}
	}
}

// The two providers a hosted request actually reaches publish under disjoint URI
// SCHEMES, which is what makes a collision between them impossible rather than
// merely absent today.
//
// Asserted structurally because it can be: the vocabulary publishes under
// `margince://` and a view under `ui://`, and neither needs a database to say so.
// A collision would otherwise be invisible — the fan-out resolves one by order
// and the losing document becomes unreachable with nothing reporting it, and the
// read would serve one provider's bytes under the other's sandbox policy.
func TestTheProductionProvidersPublishDisjointSchemes(t *testing.T) {
	// Derived from the transport's own list, so a THIRD provider is measured the
	// commit it is wired rather than the commit somebody remembers this test.
	wired := mcpResourceProviders(nil, primedViews(t, bothViews()))
	if len(wired) != 2 {
		t.Fatalf("the transport composes %d resource providers; this gate knows how to reason about the "+
			"vocabulary and the views. Add the new one's scheme below before it can collide with a view", len(wired))
	}
	for _, r := range primedViews(t, bothViews()).Resources(readerCtx()) {
		if !strings.HasPrefix(r.URI, mcp.AppURIScheme) {
			t.Errorf("the view provider publishes %s, which is outside %s — it can now collide with the query "+
				"vocabulary, and the fan-out would resolve that silently by composition order", r.URI, mcp.AppURIScheme)
		}
	}
	// The vocabulary's own scheme, named as the constant the other side of the
	// fan-out uses. If it ever publishes under `ui://` this fails here rather
	// than as a document that quietly stopped being served.
	if strings.HasPrefix(search.QuerySchemaURI, mcp.AppURIScheme) {
		t.Errorf("the query vocabulary publishes %s, inside the view scheme %s", search.QuerySchemaURI, mcp.AppURIScheme)
	}
}

// Two providers claiming one URI is a collision the fan-out resolves by order,
// which means the losing document becomes unreachable silently. The ordering is a
// defined tiebreak, not a way to notice this.
func TestNoTwoResourceProvidersClaimOneURI(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range composedResources(t).Resources(readerCtx()) {
		if seen[r.URI] {
			t.Errorf("two providers publish %s; the fan-out serves whichever was composed first and the other "+
				"document is unreachable with nothing reporting it", r.URI)
		}
		seen[r.URI] = true
	}
}
