// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// extensionEdge is the one line in this change with a security property, and
// its own comment says so: mount the composed extension routes on the
// OPERATIONAL mux and ServeMux's longest-pattern-wins rule makes "/v1/ext/…"
// beat the "/v1/" entry that carries authH.Middleware — every extension route
// would then serve with no session and no workspace. These tests hold the three
// properties that make the nesting real:
//
//  1. an extension route is behind the session middleware (asserted through the
//     assembled operational mux, not by reading the wiring);
//  2. the "/" fall-through reaches the core router untouched;
//  3. the edge mounts exactly what ComposedVerbs() declares, and vanishes when
//     there is nothing to mount.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// withComposedVerbs installs a composed set for one test and removes it after,
// so the process-global boot state one test needs is never another's.
// servedVerbs are the DECLARATIONS a unit shipped behavior for, not bare tool
// names: the served set is keyed by (unit, tool), so a helper taking names
// alone could not express which unit owns a handler — the very distinction the
// route-ownership fix rests on.
func withComposedVerbs(t *testing.T, verbs []extension.Verb, servedVerbs ...extension.Verb) {
	t.Helper()
	setComposedVerbs(verbs)
	served := make([]mcp.Tool, 0, len(servedVerbs))
	for _, v := range servedVerbs {
		served = append(served, servedTool{unit: v.Unit, name: v.Tool})
	}
	setComposedTools(served)
	t.Cleanup(func() { setComposedVerbs(nil); setComposedTools(nil) })
}

// edgeServer is the smallest Server extensionEdge reads: a tool registry, which
// is the only field it touches.
func edgeServer(t *testing.T) Server {
	t.Helper()
	return Server{toolRegistry: agents.NewRegistry(nil, auth.NewGate(fullSeat{}))}
}

// coreRouter stands in for the generated /v1 surface, so a fall-through is
// observable as "the core router answered" rather than as a 404.
//
// A named struct rather than an http.HandlerFunc, because two of the assertions
// below are IDENTITY assertions ("the edge returned the core router unchanged")
// and comparing interface values holding func types panics at runtime.
type coreRouterStub struct{ hit *string }

func (c coreRouterStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	*c.hit = r.URL.Path
	w.WriteHeader(http.StatusTeapot)
}

func coreRouter(hit *string) http.Handler { return coreRouterStub{hit: hit} }

// TestExtensionRoutesAreBehindTheSessionMiddleware is property 1, and it is
// asserted against the mux operationalMux actually assembles — not against the
// edge in isolation, because the whole failure mode is about WHERE the edge sits
// relative to authH.Middleware.
func TestExtensionRoutesAreBehindTheSessionMiddleware(t *testing.T) {
	verbs := composedFixture()
	withComposedVerbs(t, verbs, verbs...)

	// A zero pool: nothing on the path this test takes dereferences it. The
	// readiness and metrics closures capture it lazily and are never called, and
	// mux.Handler RESOLVES without serving.
	pool := &pgxpool.Pool{}
	srv := newServer(pool, discardLog(), authHandlers{}, dealsHandlers{})
	srv.toolRegistry = agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	var coreHit string
	mux := operationalMux(srv, pool, discardLog(), nil, coreRouter(&coreHit))

	// The assertion is on the RESOLVED PATTERN, and that is the whole failure
	// mode rather than a proxy for it: ServeMux picks the longest matching
	// pattern, so the only way an extension route can escape authH.Middleware is
	// for the operational mux to hold a pattern more specific than "/v1/" that
	// matches it. Every declared route must resolve through "/v1/" — the one
	// entry the session middleware wraps.
	for _, v := range verbs {
		req := httptest.NewRequest(v.Method, v.ServedPath(), strings.NewReader(`{}`))
		if _, pattern := mux.Handler(req); pattern != "/v1/" {
			t.Errorf("the operational mux resolves %s %s through %q, want \"/v1/\" — an extension route mounted "+
				"beside the core surface wins longest-pattern-match and serves with no session and no workspace",
				v.Method, v.ServedPath(), pattern)
		}
	}
	// The same pattern a CORE route resolves through, so the assertion above is
	// "one chain" rather than "some chain that happens to be named /v1/".
	if _, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/v1/deals", nil)); pattern != "/v1/" {
		t.Fatalf("a core route resolves through %q — this test's baseline is wrong", pattern)
	}

	// Deliberately NOT asserted here: that an unauthenticated call receives a
	// 401. authH.Middleware's first act is identity.Service.InstallationWorkspace,
	// which reads the database, and `check-test-lanes` forbids a unit test from
	// opening one. The structural assertion above is not a weaker stand-in for
	// that — it is the same property stated where it is decidable: the request
	// enters the chain authH.Middleware wraps, and no pattern exists that could
	// route around it.
}

// TestExtensionEdgeFallsThroughToTheCoreRouter is property 2. It is also what
// closes the one gap the parity pair structurally cannot see (see
// extparity_test.go's residual): the edge's own mux.Handle("/", next) is a
// registration MountExtensionRoutes never reports, so this is the assertion that
// it serves nothing of its own.
func TestExtensionEdgeFallsThroughToTheCoreRouter(t *testing.T) {
	verb := unitVerb("alpha", "alpha_sync", extension.TierAutoExecute, extension.ScopeRead)
	withComposedVerbs(t, []extension.Verb{verb}, verb)

	var coreHit string
	edged := extensionEdge(edgeServer(t), discardLog())(coreRouter(&coreHit))

	for _, path := range []string{"/v1/deals", "/v1/ext", "/v1/ext/alpha", "/v1/ext/alpha/other", "/healthz"} {
		coreHit = ""
		rec := httptest.NewRecorder()
		edged.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if coreHit != path {
			t.Errorf("%s did not reach the core router (hit %q, status %d)", path, coreHit, rec.Code)
		}
	}

	// And the declared route does NOT fall through — otherwise the fall-through
	// would be "everything", which passes the loop above and mounts nothing.
	coreHit = ""
	rec := httptest.NewRecorder()
	edged.ServeHTTP(rec, httptest.NewRequest(verb.Method, verb.ServedPath(), strings.NewReader(`{}`)))
	if coreHit != "" {
		t.Fatalf("the declared extension route fell through to the core router")
	}
	if rec.Code == http.StatusTeapot {
		t.Fatal("the declared extension route was served by the core router")
	}
}

// TestExtensionEdgeMountsWhatComposedVerbsDeclares is property 3, in both
// directions: what is declared is reachable through the edge, and an edge with
// nothing declared is the identity function rather than a mux that could shadow
// a core route.
func TestExtensionEdgeMountsWhatComposedVerbsDeclares(t *testing.T) {
	verbs := composedFixture()
	withComposedVerbs(t, verbs, verbs...)

	var coreHit string
	core := coreRouter(&coreHit)
	edged := extensionEdge(edgeServer(t), discardLog())(core)
	for _, v := range verbs {
		coreHit = ""
		rec := httptest.NewRecorder()
		edged.ServeHTTP(rec, httptest.NewRequest(v.Method, v.ServedPath(), strings.NewReader(`{}`)))
		if coreHit != "" {
			t.Errorf("%s %s is declared but the edge let it fall through to the core router", v.Method, v.ServedPath())
		}
	}

	t.Run("an empty composed set leaves the core router untouched", func(t *testing.T) {
		setComposedVerbs(nil)
		if got := extensionEdge(edgeServer(t), discardLog())(core); got != core {
			t.Fatal("the edge wrapped the core router with nothing to mount")
		}
	})

	t.Run("a role with no tool registry leaves the core router untouched", func(t *testing.T) {
		setComposedVerbs(verbs)
		if got := extensionEdge(Server{}, discardLog())(core); got != core {
			t.Fatal("the edge mounted routes with no registry to invoke through")
		}
	})
}

// servedTool is the smallest thing setComposedTools accepts: a unit and a
// name, so composedServedVerbs reports the pair as served. Nothing here is
// invoked — these tests are about routing, and invocation through the admission
// gate is extensiontools_test.go's subject.
type servedTool struct {
	unit extension.Name
	name string
}

func (t servedTool) Spec() mcp.ToolSpec { return mcp.ToolSpec{Name: t.name} }

// OwningUnit satisfies mcp.UnitScopedTool: an adapted tool must be able to name
// the unit that shipped it, or no route counts it as implemented.
func (t servedTool) OwningUnit() string { return string(t.unit) }

func (servedTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
