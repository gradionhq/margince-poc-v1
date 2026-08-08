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
// (MountExtensionRoutes on a real ServeMux), and both are mutation-checked: the
// "fails in both directions" subtests below add and drop a declaration and
// require the sweep to notice.

import (
	"context"
	"encoding/json"
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
func mountForTest(t *testing.T, verbs []extension.Verb) (*http.ServeMux, []string) {
	t.Helper()
	mux := http.NewServeMux()
	patterns, err := MountExtensionRoutes(mux, verbs, noopInvoker)
	if err != nil {
		t.Fatalf("mounting the declared extension routes: %v", err)
	}
	return mux, patterns
}

// declaredPatterns is the DECLARATION side, derived the one way the composition
// root derives it: METHOD + " " + route, off each declared operation.
func declaredPatterns(verbs []extension.Verb) []string {
	out := make([]string, 0, len(verbs))
	for _, v := range verbs {
		out = append(out, v.Method+" "+v.Route)
	}
	slices.Sort(out)
	return out
}

// composedFixture is a two-unit declaration set: one unit with two operations
// on distinct methods of distinct routes, one with a single operation. It is
// deliberately not the live tree's set — the sweep has to hold for an
// installation with several units, and the live set is checked separately by
// TestTheLiveComposedSetMountsEveryVerbItDeclares.
func composedFixture() []extension.Verb {
	crm := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	audit := unitVerb("alpha", "audit_records", extension.TierAutoExecute, extension.ScopeRead)
	audit.Method = http.MethodPut
	beta := unitVerb("beta", "beta_ping", extension.TierAutoExecute, extension.ScopeRead)
	return []extension.Verb{crm, audit, beta}
}

func TestEveryDeclaredExtensionVerbHasARegistration(t *testing.T) {
	verbs := composedFixture()
	mux, patterns := mountForTest(t, verbs)
	mounted := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		mounted[p] = true
	}
	for _, v := range verbs {
		pattern := v.Method + " " + v.Route
		if !mounted[pattern] {
			t.Errorf("%s (%s, operation %s) is declared in the merged contract but no route is registered for it, "+
				"so the contract publishes an endpoint that answers 404. Mount it in MountExtensionRoutes, or remove "+
				"the operation from extensions/%s/api/%s.", pattern, v.Unit, v.OperationID, v.Unit, v.Contract)
			continue
		}
		// Registered is not the same as reachable: a pattern can be recorded
		// and still not resolve if the mux never got it. Ask the mux.
		req := httptest.NewRequest(v.Method, v.Route, strings.NewReader(`{}`))
		if _, resolved := mux.Handler(req); resolved == "" {
			t.Errorf("%s reports as mounted but the router resolves nothing for it", pattern)
		}
	}

	t.Run("and it fails when a declaration loses its registration", func(t *testing.T) {
		// The mutation: mount only the FIRST verb, then sweep all three. This
		// is the direction that would otherwise pass silently, because nothing
		// in a ServeMux complains about a route that was never added.
		mux := http.NewServeMux()
		patterns, err := MountExtensionRoutes(mux, verbs[:1], noopInvoker)
		if err != nil {
			t.Fatal(err)
		}
		missing := 0
		for _, v := range verbs {
			if !slices.Contains(patterns, v.Method+" "+v.Route) {
				missing++
			}
		}
		if missing != 2 {
			t.Fatalf("the sweep found %d unregistered declarations, want 2 — it cannot see this direction", missing)
		}
	})
}

func TestEveryExtensionRegistrationHasADeclaration(t *testing.T) {
	verbs := composedFixture()
	_, patterns := mountForTest(t, verbs)
	declared := declaredPatterns(verbs)
	for _, pattern := range patterns {
		if !slices.Contains(declared, pattern) {
			t.Errorf("%s is a mounted extension route that no contract operation declares, so an authenticated "+
				"caller can reach a verb no contract granted and no unit manifest asks an operator about. "+
				"Declare the operation in the unit's api/ fragment, or stop mounting it.", pattern)
		}
	}
	if len(patterns) == 0 {
		t.Fatal("nothing was mounted — this sweep checked nothing")
	}

	t.Run("and it fails when a registration has no declaration", func(t *testing.T) {
		// The mutation: mount an extra verb the sweep's declaration set does
		// not contain. This is the direction a ServeMux is silent about too — an
		// unexpected route serves perfectly well.
		extra := unitVerb("beta", "undeclared_verb", extension.TierAutoExecute, extension.ScopeRead)
		mux := http.NewServeMux()
		patterns, err := MountExtensionRoutes(mux, append(slices.Clone(verbs), extra), noopInvoker)
		if err != nil {
			t.Fatal(err)
		}
		orphans := 0
		for _, pattern := range patterns {
			if !slices.Contains(declared, pattern) {
				orphans++
			}
		}
		if orphans != 1 {
			t.Fatalf("the sweep found %d undeclared registrations, want 1 — it cannot see this direction", orphans)
		}
	})
}

// TestTheLiveComposedSetMountsEveryVerbItDeclares runs the same pair over
// whatever this installation actually composes, so a first-party unit that ships
// an operation the mounting cannot serve fails here rather than at someone's
// boot. In the vanilla tree the composed set is empty and this asserts the empty
// case — which is the case that must also stay true.
func TestTheLiveComposedSetMountsEveryVerbItDeclares(t *testing.T) {
	verbs := ComposedVerbs()
	_, patterns := mountForTest(t, verbs)
	if got, want := len(patterns), len(verbs); got != want {
		t.Fatalf("mounted %d routes for %d declared operations", got, want)
	}
	// declaredPatterns sorts; the mount order follows the verb order, so the
	// two are compared as sets.
	got := slices.Clone(patterns)
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
	templated.Route = "/v1/ext/alpha/{id}"
	otherUnit := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	otherUnit.Route = "/v1/ext/beta/sync-contacts"
	bodyless := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
	bodyless.Method = http.MethodGet

	for name, verb := range map[string]extension.Verb{
		"a core route":             outsideNamespace,
		"a path template":          templated,
		"another unit's namespace": otherUnit,
		"a method with no body":    bodyless,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MountExtensionRoutes(http.NewServeMux(), []extension.Verb{verb}, noopInvoker); err == nil {
				t.Fatal("the route mounted; want a refusal")
			}
		})
	}

	t.Run("two units claiming one pattern", func(t *testing.T) {
		a := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeRead)
		b := unitVerb("beta", "beta_ping", extension.TierAutoExecute, extension.ScopeRead)
		b.Route = a.Route
		b.Unit = a.Unit
		_, err := MountExtensionRoutes(http.NewServeMux(), []extension.Verb{a, b}, noopInvoker)
		if err == nil || !strings.Contains(err.Error(), "both declare") {
			// ServeMux would PANIC on the duplicate; the named refusal is what
			// tells an operator which units to talk to.
			t.Fatalf("err = %v, want the duplicate-pattern refusal", err)
		}
	})

	t.Run("no registry to invoke through", func(t *testing.T) {
		if _, err := MountExtensionRoutes(http.NewServeMux(), composedFixture(), nil); err == nil {
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
	if _, err := MountExtensionRoutes(mux, verbs, func(_ context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
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
