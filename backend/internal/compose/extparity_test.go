// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The extension twin of the REST guarantee.
//
// For a core operation, `var _ crmcontracts.ServerInterface = Server{}`
// (server.go) makes "declared but not handled" a COMPILE error. Extensions
// cannot have that: crmcontracts is generated from the base contract, which is
// installation-independent by design, so an extension operation is never a
// method on that interface. These two tests are the runtime equivalent, and
// they exist as a PAIR because either alone is satisfied by a surface that is
// wrong in the other direction:
//
//   - a declared verb with no registration is a route the merged contract
//     publishes, the docs describe and a client generates a call for, which
//     answers 404;
//   - a registration nothing declares is a reachable authenticated endpoint no
//     contract, no client type, no doc and no unit manifest knows about — so
//     nothing asks an operator to resolve it either.
//
// Both are asserted against the SAME mounting the composition root performs
// (MountExtensionRoutes on a real ServeMux), through the SAME two functions the
// parent assertions use (unregisteredDeclarations, undeclaredRegistrations), so
// the mutation subtests exercise the sweep rather than an equivalent loop.
//
// THE RESIDUAL, stated because the pair is weaker in one direction than in the
// other and the difference is structural. Direction 1 is checkable outright: a
// declaration is a value, and MountExtensionRoutes either mounted it or did not
// — and the mux.Handler probe confirms the router really resolves it, not just
// that a pattern was recorded. Direction 2 can only see what MountExtensionRoutes
// REPORTS. A mux.Handle call whose pattern is never appended to the returned
// slice is invisible to it, and extensionEdge itself does exactly that with
// mux.Handle("/", next). So direction 2 holds "everything this seam admits to
// mounting was declared", not "nothing else is mounted" — the latter is
// uncheckable because a ServeMux cannot be enumerated. What closes the gap for
// the one unreported registration that exists is
// TestExtensionEdgeFallsThroughToTheCoreRouter, which asserts that "/" reaches
// next rather than serving anything of its own.
//
// A third state sits between "declared" and "registered": a verb the contract
// declares that no unit shipped behavior for. It is mounted and answers 501.
// The sweep distinguishes it rather than collapsing it into either side — see
// MountedRoute.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// noopInvoker stands in for the tool registry. These tests are about which
// routes exist, not about what running one does — invocation through the
// registry's admission gate is covered in extensiontools_test.go.
func noopInvoker(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// mountForTest mounts verbs onto a fresh mux and returns both, so a test can
// assert over the patterns AND make a real request against them.
func mountForTest(t *testing.T, verbs []extension.Verb, served map[string]bool) (*http.ServeMux, []MountedRoute) {
	t.Helper()
	mux := http.NewServeMux()
	routes, err := MountExtensionRoutes(mux, verbs, served, noopInvoker)
	if err != nil {
		t.Fatalf("mounting the declared extension routes: %v", err)
	}
	return mux, routes
}

// allServed is the "every declared verb has behavior" case, which is what the
// parity sweeps are about; the contract-only case has its own test.
func allServed(verbs []extension.Verb) map[string]bool {
	served := make(map[string]bool, len(verbs))
	for _, v := range verbs {
		served[v.Tool] = true
	}
	return served
}

// declaredPatterns is the DECLARATION side, derived the one way the composition
// root derives it: METHOD + " " + the SERVED path.
//
// ServedPath, not Route: a contract path is relative to the contract's own
// servers url (which ends in /v1) and a ServeMux pattern is not, so the two
// sides of this sweep have to agree on which spelling they carry. They agree by
// both going through the one function that converts.
func declaredPatterns(verbs []extension.Verb) []string {
	out := make([]string, 0, len(verbs))
	for _, v := range verbs {
		out = append(out, declaredPattern(v))
	}
	slices.Sort(out)
	return out
}

// declaredPattern is the single conversion both directions of the sweep use.
func declaredPattern(v extension.Verb) string { return v.Method + " " + v.ServedPath() }

// unregisteredDeclarations is DIRECTION 1 of the sweep, as a function, so the
// parent assertion and the mutation subtest run the same code. Each string it
// returns is one violation, already phrased for a reader.
func unregisteredDeclarations(mux *http.ServeMux, verbs []extension.Verb, routes []MountedRoute) []string {
	mounted := make(map[string]bool, len(routes))
	for _, route := range routes {
		mounted[route.Pattern] = true
	}
	var violations []string
	for _, v := range verbs {
		pattern := declaredPattern(v)
		if !mounted[pattern] {
			violations = append(violations, fmt.Sprintf(
				"%s (%s, operation %s) is declared in the merged contract but no route is registered for it, "+
					"so the contract publishes an endpoint that answers 404. Mount it in MountExtensionRoutes, or "+
					"remove the operation from extensions/%s/api/%s.", pattern, v.Unit, v.OperationID, v.Unit, v.Contract))
			continue
		}
		// Registered is not the same as reachable: a pattern can be recorded and
		// still not resolve if the mux never got it. Ask the mux.
		req := httptest.NewRequest(v.Method, v.ServedPath(), strings.NewReader(`{}`))
		if _, resolved := mux.Handler(req); resolved == "" {
			violations = append(violations, fmt.Sprintf("%s reports as mounted but the router resolves nothing for it", pattern))
		}
	}
	return violations
}

// undeclaredRegistrations is DIRECTION 2, subject to the residual in this
// file's header: it sees what the mounting reports, which is everything the
// mounting adds ON TOP of the caller's own fall-through.
func undeclaredRegistrations(verbs []extension.Verb, routes []MountedRoute) []string {
	declared := declaredPatterns(verbs)
	var violations []string
	for _, route := range routes {
		if !slices.Contains(declared, route.Pattern) {
			violations = append(violations, fmt.Sprintf(
				"%s is a mounted extension route that no contract operation declares, so an authenticated caller "+
					"can reach a verb no contract granted and no unit manifest asks an operator about. Declare the "+
					"operation in the unit's api/ fragment, or stop mounting it.", route.Pattern))
		}
	}
	return violations
}

func TestEveryDeclaredExtensionVerbHasARegistration(t *testing.T) {
	verbs := composedFixture()
	mux, routes := mountForTest(t, verbs, allServed(verbs))
	for _, violation := range unregisteredDeclarations(mux, verbs, routes) {
		t.Error(violation)
	}

	t.Run("and it fails when a declaration loses its registration", func(t *testing.T) {
		// The mutation: mount only the FIRST verb, then run the REAL sweep over
		// all three. This is the direction that would otherwise pass silently,
		// because nothing in a ServeMux complains about a route never added.
		mux, routes := mountForTest(t, verbs[:1], allServed(verbs))
		if got := unregisteredDeclarations(mux, verbs, routes); len(got) != 2 {
			t.Fatalf("the sweep reported %d violations, want 2 — it cannot see this direction:\n%s",
				len(got), strings.Join(got, "\n"))
		}
	})
}

func TestEveryExtensionRegistrationHasADeclaration(t *testing.T) {
	verbs := composedFixture()
	_, routes := mountForTest(t, verbs, allServed(verbs))
	if len(routes) == 0 {
		t.Fatal("nothing was mounted — this sweep checked nothing")
	}
	for _, violation := range undeclaredRegistrations(verbs, routes) {
		t.Error(violation)
	}

	t.Run("and it fails when a registration has no declaration", func(t *testing.T) {
		// The mutation: mount an extra verb the sweep's declaration set does not
		// contain, then run the REAL sweep. A ServeMux is silent about this too
		// — an unexpected route serves perfectly well.
		extra := unitVerb("beta", "undeclared_verb", extension.TierAutoExecute, extension.ScopeRead)
		_, routes := mountForTest(t, append(slices.Clone(verbs), extra), allServed(verbs))
		if got := undeclaredRegistrations(verbs, routes); len(got) != 1 {
			t.Fatalf("the sweep reported %d violations, want 1 — it cannot see this direction:\n%s",
				len(got), strings.Join(got, "\n"))
		}
	})
}

// TestTheSweepTellsAContractOnlyVerbFromAMissingOne is the third state. Before
// it, "mounted" meant "handled", so crm-hello's handler-less hello_ping was
// mounted like any other route and its handler reached Invoke for a verb the
// registry has never heard of — an opaque 500 and an "unhandled error" log line
// per call, on a fixture the CI extension lane copies into extensions/.
func TestTheSweepTellsAContractOnlyVerbFromAMissingOne(t *testing.T) {
	verbs := composedFixture()
	// Only the first verb has behavior; the other two are contract-only.
	served := map[string]bool{verbs[0].Tool: true}
	mux, routes := mountForTest(t, verbs, served)

	// Every declaration is still registered — a contract-only verb is NOT a
	// missing one, and reporting it as missing would tell a unit author to
	// mount something already mounted.
	if got := unregisteredDeclarations(mux, verbs, routes); len(got) != 0 {
		t.Fatalf("a contract-only declaration was reported as unregistered:\n%s", strings.Join(got, "\n"))
	}
	implemented := 0
	for _, route := range routes {
		if route.Implemented {
			implemented++
		}
	}
	if implemented != 1 {
		t.Fatalf("%d routes report as implemented, want 1 — the sweep collapses the third state", implemented)
	}

	// And the state is what the response says it is.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/ext/alpha/audit-records", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("a contract-only route answered %d, want 501 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "not_implemented") || !strings.Contains(rec.Body.String(), "audit_recordsOp") {
		t.Fatalf("the 501 does not name the declared-but-unimplemented operation: %s", rec.Body)
	}
	// The handled one still runs.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ext/alpha/sync-contacts", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("the implemented route answered %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

// TestTheLiveComposedSetHasNoUnhandledInvocation: the live tree is what CI's
// extension lane runs, and crm-hello is copied into it there. A declared verb
// with no served tool must never reach Invoke.
func TestTheLiveComposedSetHasNoUnhandledInvocation(t *testing.T) {
	verbs := ComposedVerbs()
	served := composedToolNames()
	reached := ""
	mux := http.NewServeMux()
	routes, err := MountExtensionRoutes(mux, verbs, served, func(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
		reached = name
		return json.RawMessage(`{}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Implemented {
			continue
		}
		reached = ""
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.Verb.Method, route.Verb.ServedPath(), strings.NewReader(`{}`)))
		if reached != "" {
			t.Errorf("%s has no served tool but its route invoked %q", route.Pattern, reached)
		}
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s answered %d, want 501", route.Pattern, rec.Code)
		}
	}
}

// composedFixture is a two-unit declaration set: one unit with two operations
// on distinct methods of distinct routes, one with a single operation. It is
// deliberately not the live tree's set — the sweeps have to hold for an
// installation with several units, and the live set is checked separately by
// TestTheLiveComposedSetMountsEveryVerbItDeclares.
func composedFixture() []extension.Verb {
	crm := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	audit := unitVerb("alpha", "audit_records", extension.TierAutoExecute, extension.ScopeRead)
	audit.Method = http.MethodPut
	beta := unitVerb("beta", "beta_ping", extension.TierAutoExecute, extension.ScopeRead)
	return []extension.Verb{crm, audit, beta}
}

// TestTheLiveComposedSetMountsEveryVerbItDeclares runs the same pair over
// whatever this installation actually composes, so a first-party unit that ships
// an operation the mounting cannot serve fails here rather than at someone's
// boot. In the vanilla tree the composed set is empty and this asserts the empty
// case — which is the case that must also stay true.
func TestTheLiveComposedSetMountsEveryVerbItDeclares(t *testing.T) {
	verbs := ComposedVerbs()
	_, routes := mountForTest(t, verbs, allServed(verbs))
	if got, want := len(routes), len(verbs); got != want {
		t.Fatalf("mounted %d routes for %d declared operations", got, want)
	}
	// declaredPatterns sorts; the mount order follows the verb order, so the
	// two are compared as sets.
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Pattern)
	}
	slices.Sort(got)
	if slices.Compare(declaredPatterns(verbs), got) != 0 {
		t.Fatalf("mounted %v, declared %v", got, declaredPatterns(verbs))
	}
}

// TestAnUndeclaredRouteCannotBeMounted: the mounting refuses what it cannot
// serve honestly, so the parity pair is not the only thing standing between a
// bad declaration and a live endpoint.
func TestAnUndeclaredRouteCannotBeMounted(t *testing.T) {
	outsideNamespace := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	outsideNamespace.Route = "/v1/deals"
	templated := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	templated.Route = "/ext/alpha/{id}"
	otherUnit := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	otherUnit.Route = "/ext/beta/sync-contacts"
	bodyless := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	bodyless.Method = http.MethodGet

	for name, verb := range map[string]extension.Verb{
		"a core route":             outsideNamespace,
		"a path template":          templated,
		"another unit's namespace": otherUnit,
		"a method with no body":    bodyless,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MountExtensionRoutes(http.NewServeMux(), []extension.Verb{verb}, nil, noopInvoker); err == nil {
				t.Fatal("the route mounted; want a refusal")
			}
		})
	}

	t.Run("two units claiming one pattern", func(t *testing.T) {
		a := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
		b := unitVerb("beta", "beta_ping", extension.TierAutoExecute, extension.ScopeRead)
		b.Route = a.Route
		b.Unit = a.Unit
		_, err := MountExtensionRoutes(http.NewServeMux(), []extension.Verb{a, b}, nil, noopInvoker)
		if err == nil || !strings.Contains(err.Error(), "both declare") {
			// ServeMux would PANIC on the duplicate; the named refusal is what
			// tells an operator which units to talk to.
			t.Fatalf("err = %v, want the duplicate-pattern refusal", err)
		}
	})

	t.Run("no registry to invoke through", func(t *testing.T) {
		if _, err := MountExtensionRoutes(http.NewServeMux(), composedFixture(), nil, nil); err == nil {
			t.Fatal("routes mounted without a registry; want the refusal")
		}
	})
}

// TestAMountedRouteInvokesItsDeclaredVerb: the route is not merely present, it
// dispatches to the verb the contract named — and it dispatches through the
// invoker seam, which in the composition root IS the registry's admission gate.
func TestAMountedRouteInvokesItsDeclaredVerb(t *testing.T) {
	verbs := composedFixture()
	var called string
	mux := http.NewServeMux()
	if _, err := MountExtensionRoutes(mux, verbs, allServed(verbs), func(_ context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
		called = name
		return json.RawMessage(`{"args":` + string(in) + `}`), nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/ext/alpha/audit-records", strings.NewReader(`{"k":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if called != "audit_records" {
		t.Fatalf("invoked %q, want the declared verb audit_records", called)
	}
	if got := rec.Body.String(); got != `{"args":{"k":1}}` {
		t.Fatalf("body = %s, want the tool's own bytes verbatim", got)
	}

	// The method is part of the declaration, so a declared PUT does not answer
	// a POST. Without method-and-path patterns this would be a 200 on a verb
	// the contract said was a PUT.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ext/alpha/audit-records", strings.NewReader(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d on an undeclared method, want 405", rec.Code)
	}

	// An absent body is the empty object, not a refusal: a tool taking no
	// arguments must be callable with no body.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ext/beta/beta-ping", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"args":{}}` {
		t.Fatalf("status = %d body = %s, want 200 and the empty-object default", rec.Code, rec.Body)
	}

	// And a body that is not JSON is refused before the tool runs, so a tool's
	// own decode is never handed something no decoder can read.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ext/beta/beta-ping", strings.NewReader(`{nope`)))
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d on a malformed body, want a 4xx refusal (body %s)", rec.Code, rec.Body)
	}
}
