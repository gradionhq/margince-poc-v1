// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// wellFormed is the declaration every case below mutates one field of, so a
// failure names the field rather than the whole value.
// wellFormed is a WRITE-scoped declaration and therefore carries an RBAC
// object: a mutating operation that names nothing a role document can withhold
// is refused (see Verb.Validate), so an objectless one would not be well formed.
// The read-scoped, objectless shape is asserted separately below — it is the
// other legitimate case, not this one with a field dropped.
func wellFormed() Verb {
	return Verb{
		Unit:           "crm-demo",
		Contract:       "crm.yaml",
		OperationID:    "crmDemoSync",
		Route:          "/ext/crm-demo/sync",
		Method:         http.MethodPost,
		Tool:           "demo_sync",
		Title:          "Sync the demo",
		Description:    "Bring the demo records in step, and report which ones moved.",
		Version:        "1.0.0",
		Tier:           TierAutoExecute,
		RequestedScope: ScopeWrite,
		RbacObject:     "ext_crm_demo_widget",
		RbacAction:     RbacUpdate,
		InputSchema:    json.RawMessage(`{"type":"object"}`),
		OutputSchema:   json.RawMessage(`{"type":"object"}`),
	}
}

func TestVerbValidateAcceptsAWellFormedDeclaration(t *testing.T) {
	if err := wellFormed().Validate(); err != nil {
		t.Fatalf("a well-formed declaration must validate: %v", err)
	}
	// Every optional field absent. A tool with no title is listed under its
	// verb, one with no description is refused only where it is SERVED, and a
	// tool with no schemas advertises the default object — none of which is the
	// grammar's business.
	minimal := wellFormed()
	minimal.Title, minimal.Description = "", ""
	minimal.InputSchema, minimal.OutputSchema = nil, nil
	// Read-scoped and objectless, because THAT is the minimal legitimate
	// declaration: the RBAC pair is optional only for an operation that changes
	// nothing.
	minimal.RequestedScope = ScopeRead
	minimal.RbacObject, minimal.RbacAction = "", ""
	if err := minimal.Validate(); err != nil {
		t.Fatalf("a minimal declaration must validate: %v", err)
	}
	// And the RBAC object, which is optional but namespaced when present.
	gated := wellFormed()
	gated.RbacObject = "ext_crm_demo_widget"
	gated.RbacAction = RbacRead
	if err := gated.Validate(); err != nil {
		t.Fatalf("a namespaced RBAC object must validate: %v", err)
	}
	// A mutating operation is required to name an object (see Validate), so the
	// accepted shape has to be pinned too — otherwise the refusal could be
	// tightened into "no extension may write" and every test would still pass.
	for _, scope := range []Scope{ScopeWrite, ScopeDraft} {
		mutating := wellFormed()
		mutating.RequestedScope = scope
		mutating.RbacObject, mutating.RbacAction = "ext_crm_demo_widget", RbacUpdate
		if err := mutating.Validate(); err != nil {
			t.Fatalf("a %s operation naming an object must validate: %v", string(scope), err)
		}
	}
	// And a READ operation still needs none: yogi, de and crm-hello own no
	// records, and requiring an object of them would refuse the common case.
	readOnly := wellFormed()
	readOnly.RequestedScope = ScopeRead
	readOnly.RbacObject, readOnly.RbacAction = "", ""
	if err := readOnly.Validate(); err != nil {
		t.Fatalf("a read operation owning no records must validate: %v", err)
	}
}

// TestVerbValidateRefusals: one case per rule, because the refusals are what
// this type is FOR — it is read by the generator that derives it and by the boot
// that serves it, and a rule missing from either side is a declaration accepted
// at gen time and refused (or worse, served) at boot.
func TestVerbValidateRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Verb){
		"a unit name outside the grammar": func(v *Verb) { v.Unit = "Bad_Unit" },
		"no source contract":              func(v *Verb) { v.Contract = "" },
		"no operationId":                  func(v *Verb) { v.OperationID = "" },
		"a core route":                    func(v *Verb) { v.Route = "/v1/deals" },
		"a route with a path template":    func(v *Verb) { v.Route = "/ext/crm-demo/{id}" },
		// A contract path is relative to the document's own servers url, which
		// already ends in /v1. A declaration spelling the prefix itself would
		// resolve to https://host/v1/v1/ext/… for every consumer of the
		// published contract, so the grammar refuses it rather than serving one
		// spelling and publishing another.
		"a route that spells the API base path": func(v *Verb) { v.Route = APIBasePath + "/ext/crm-demo/sync" },
		"a route with no leaf segment":          func(v *Verb) { v.Route = "/ext/crm-demo" },
		"a route with an upper-case segment":    func(v *Verb) { v.Route = "/ext/crm-demo/Sync" },
		"another unit's namespace":              func(v *Verb) { v.Route = "/ext/other/sync" },
		"a method outside the admitted set":     func(v *Verb) { v.Method = http.MethodHead },
		"a lower-case method":                   func(v *Verb) { v.Method = "post" },
		"no method":                             func(v *Verb) { v.Method = "" },
		// The pairing rules. wellFormed is write-scoped, so a bare method swap is
		// already the mutating case for GET; the read-scoped cases set the scope
		// (and drop the RBAC pair, which a read does not need) so each row fails on
		// the rule it is named for rather than on the mutating-needs-an-object one.
		"a GET that requests a mutating scope": func(v *Verb) { v.Method = http.MethodGet },
		"a read-scoped PUT": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodPut, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
		},
		"a read-scoped PATCH": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodPatch, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
		},
		"a read-scoped DELETE": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodDelete, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
		},
		// A query string is flat text pairs, so an argument with structure has no
		// honest encoding. Both shapes, because refusing only objects would leave
		// arrays — whose every convention (repeated keys, comma joins) is a second
		// contract the published schema does not describe.
		"a GET whose argument is an object": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodGet, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
			v.InputSchema = json.RawMessage(`{"type":"object","properties":{"filter":{"type":"object"}}}`)
		},
		"a DELETE whose argument is an array": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodDelete, ScopeWrite
			v.RbacAction = RbacDelete
			v.InputSchema = json.RawMessage(`{"type":"object","properties":{"ids":{"type":"array"}}}`)
		},
		// A POST is unaffected by the query rule: it reads a body, so structure is
		// exactly what it is for. Asserted as a refusal's ABSENCE would not fit this
		// table, so it lives in TestABodyMethodMayTakeStructuredArguments below.
		"a GET whose argument declares no type": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodGet, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
			v.InputSchema = json.RawMessage(`{"type":"object","properties":{"filter":{}}}`)
		},
		// `required` naming an argument the properties do not declare: no call could
		// ever satisfy the route. Checked HERE and not only at the serving seam so
		// Validate is the whole rule for a bodyless declaration — the mount PANICS
		// on a refusal, so a shape Validate blessed and the mount rejected was a
		// boot crash rather than a named generation failure.
		"a GET requiring an argument it does not declare": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodGet, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
			v.InputSchema = json.RawMessage(
				`{"type":"object","properties":{"id":{"type":"string"}},"required":["missing"]}`)
		},
		"a GET whose required is not a list": func(v *Verb) {
			v.Method, v.RequestedScope = http.MethodGet, ScopeRead
			v.RbacObject, v.RbacAction = "", ""
			v.InputSchema = json.RawMessage(
				`{"type":"object","properties":{"id":{"type":"string"}},"required":"id"}`)
		},
		"a tool verb outside the grammar": func(v *Verb) { v.Tool = "Demo-Sync" },
		"no tool verb":                    func(v *Verb) { v.Tool = "" },
		"no version":                      func(v *Verb) { v.Version = "" },
		// The version is rendered like the title and the description — the
		// composer emits it as the tool's `schema_version` — so it is held to
		// the same three rules, and each has its own row for the same reason
		// the title's do: emptiness is the one fault an author notices anyway.
		"a framed version":                      func(v *Verb) { v.Version = " 1.0.0 " },
		"a non-printable version":               func(v *Verb) { v.Version = "1.0.0\n0" },
		"a version that is not valid UTF-8":     func(v *Verb) { v.Version = "1.0.0\xff" },
		"a blank title":                         func(v *Verb) { v.Title = "   " },
		"a framed title":                        func(v *Verb) { v.Title = " Sync " },
		"a non-printable title":                 func(v *Verb) { v.Title = "Sync\tit" },
		"a title that is not valid UTF-8":       func(v *Verb) { v.Title = "Sync\xffit" },
		"a blank description":                   func(v *Verb) { v.Description = "   " },
		"a non-printable description":           func(v *Verb) { v.Description = "Syncs\tit." },
		"a tier no extension may request":       func(v *Verb) { v.Tier = "dynamic" },
		"no tier":                               func(v *Verb) { v.Tier = "" },
		"a scope outside the vocabulary":        func(v *Verb) { v.RequestedScope = "admin" },
		"no scope":                              func(v *Verb) { v.RequestedScope = "" },
		"a scalar input schema":                 func(v *Verb) { v.InputSchema = json.RawMessage(`"scalar"`) },
		"an input schema that is not an object": func(v *Verb) { v.InputSchema = json.RawMessage(`{"type":"array"}`) },
		"a malformed output schema":             func(v *Verb) { v.OutputSchema = json.RawMessage(`{bad`) },
		"an unnamespaced RBAC object":           func(v *Verb) { v.RbacObject = "widget" },
		"another unit's RBAC object":            func(v *Verb) { v.RbacObject = "ext_other_widget" },
		// The pair. An object with no action would have to be enforced with a
		// guessed verb; an action with no object names no grant at all. Both
		// halves are refusals rather than defaults, because either default
		// would be governance nobody wrote.
		"an object with no action": func(v *Verb) {
			v.RbacObject, v.RbacAction = "ext_crm_demo_widget", ""
		},
		"an object with an action outside the four": func(v *Verb) {
			v.RbacObject, v.RbacAction = "ext_crm_demo_widget", "list"
		},
		// Read-scoped on purpose: with a mutating scope this would be refused by
		// the mutating-needs-an-object rule instead, and the case would stop
		// testing the rule it is named for.
		"an action with no object": func(v *Verb) {
			v.RequestedScope, v.RbacObject, v.RbacAction = ScopeRead, "", RbacRead
		},
		// The class R1 shipped: an operation that CHANGES state under no object
		// at all. Both mutating scopes, because a rule that covered only `write`
		// would leave `draft` — which also persists — expressible.
		"a write-scoped operation with no object": func(v *Verb) {
			v.RequestedScope, v.RbacObject, v.RbacAction = ScopeWrite, "", ""
		},
		"a draft-scoped operation with no object": func(v *Verb) {
			v.RequestedScope, v.RbacObject, v.RbacAction = ScopeDraft, "", ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := wellFormed()
			mutate(&v)
			if err := v.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want a refusal for %s", name)
			}
		})
	}
}

// TestAMutatingGetIsRefusedForTheSeatCeilingsReason: the one refusal in this file
// that is a security boundary rather than a grammar, so it is pinned by its
// REASON and not just by failing. Admitting GET is what made a mutating GET
// expressible; the human seat ceiling classifies a mutation by method
// (identity.serveAsHuman), so this declaration would hand a read seat a write.
// A future edit that relaxed it into a warning, or reordered it behind a rule
// that happens to catch today's fixtures, would pass a bare err != nil check.
func TestAMutatingGetIsRefusedForTheSeatCeilingsReason(t *testing.T) {
	for _, scope := range []Scope{ScopeWrite, ScopeDraft, ScopeSend, ScopeEnrich} {
		v := wellFormed()
		v.Method = http.MethodGet
		v.RequestedScope = scope
		err := v.Validate()
		if err == nil {
			t.Fatalf("a GET requesting %q validated", string(scope))
		}
		// The scope it asked for and the ceiling it would defeat — an author
		// reading only "invalid method" would reach for a different fix.
		for _, want := range []string{"GET", string(scope), "seat"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %q does not mention %q: %v", string(scope), want, err)
			}
		}
	}
}

// TestABodyMethodMayTakeStructuredArguments: the query-encodability rule must
// bind ONLY the bodyless methods. A POST reads a body, where structure is the
// point, so a rule that leaked across would refuse the shape the body path
// exists to serve — and it would do so silently, since every existing
// declaration is flat today and no other test would notice.
func TestABodyMethodMayTakeStructuredArguments(t *testing.T) {
	nested := json.RawMessage(
		`{"type":"object","properties":{"filter":{"type":"object"},"ids":{"type":"array"}}}`)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		v := wellFormed()
		v.Method = method
		v.InputSchema = nested
		if err := v.Validate(); err != nil {
			t.Errorf("a %s taking structured arguments must validate: %v", method, err)
		}
	}
}

// TestTheUnitNamespaceIsCheckedWithTheSqlSpelling: a unit name may hold hyphens
// (it is a URL path segment) but a SQL identifier may not, so the RBAC object's
// namespace uses the underscored form while the route's uses the hyphenated one.
// The two must not be confused in either direction.
func TestTheUnitNamespaceIsCheckedWithTheSqlSpelling(t *testing.T) {
	v := wellFormed()
	v.RbacAction = RbacRead
	v.RbacObject = "ext_crm-demo_widget" // the ROUTE spelling, in a SQL identifier
	if err := v.Validate(); err == nil {
		t.Fatal("a hyphenated RBAC object validated; no unquoted SQL identifier holds a hyphen")
	}
	v.RbacObject = "ext_crm_demo_widget"
	if err := v.Validate(); err != nil {
		t.Fatalf("the underscored spelling must validate: %v", err)
	}

	underscored := wellFormed()
	underscored.Route = "/ext/crm_demo/sync" // the SQL spelling, in a route
	if err := underscored.Validate(); err == nil {
		t.Fatal("a route naming the underscored unit validated; the unit name IS the path segment")
	}
}

// TestARouteMaySpellItsLeafEitherWay: a leaf segment holds hyphens or
// underscores, because a verb is snake_case and a URL convention is kebab-case,
// and forcing one on a unit author would be a rule with no property behind it.
func TestARouteMaySpellItsLeafEitherWay(t *testing.T) {
	for _, route := range []string{
		"/ext/crm-demo/sync",
		"/ext/crm-demo/sync-records",
		"/ext/crm-demo/sync_records",
		"/ext/crm-demo/records/sync",
	} {
		v := wellFormed()
		v.Route = route
		if err := v.Validate(); err != nil {
			t.Errorf("route %q must validate: %v", route, err)
		}
	}
	for _, route := range []string{
		"/ext/crm-demo/",
		"/ext/crm-demo//sync",
		"/ext/crm-demo/sync/",
		"v1/ext/crm-demo/sync",
		"/ext/crm-demo/sync?x=1",
	} {
		v := wellFormed()
		v.Route = route
		if err := v.Validate(); err == nil {
			t.Errorf("route %q validated; want a refusal", route)
		}
	}
}

// TestServedPathPutsTheBasePathBackExactlyOnce: Route is the CONTRACT's
// spelling, ServedPath is the server's, and the whole point of deriving the
// second from the first is that they cannot disagree. A field pair could.
func TestServedPathPutsTheBasePathBackExactlyOnce(t *testing.T) {
	v := wellFormed()
	if got, want := v.ServedPath(), "/v1/ext/crm-demo/sync"; got != want {
		t.Fatalf("ServedPath() = %q, want %q", got, want)
	}
	if strings.HasPrefix(v.Route, APIBasePath) {
		t.Fatalf("Route %q carries the base path — it is the contract's spelling and the contract's servers url already has it", v.Route)
	}
	if strings.Count(v.ServedPath(), APIBasePath+"/") != 1 {
		t.Fatalf("ServedPath() = %q carries the base path other than exactly once", v.ServedPath())
	}
	// The conversion is total over every route the grammar admits, so no
	// caller ever has to decide whether to prepend. The RBAC pair is dropped
	// (and the scope with it) because this loop re-homes the verb to other
	// units: an object is namespaced to ITS unit, so carrying crm-demo's along
	// would fail on a rule this test is not about.
	v.RequestedScope = ScopeRead
	v.RbacObject, v.RbacAction = "", ""
	for unit, route := range map[Name]string{"a": "/ext/a/b", "a-b": "/ext/a-b/c_d", "deep": "/ext/deep/b/c/d"} {
		v.Unit = unit
		v.Route = route
		if err := v.Validate(); err != nil {
			t.Fatalf("route %q must validate: %v", route, err)
		}
		if got, want := v.ServedPath(), APIBasePath+route; got != want {
			t.Errorf("ServedPath() = %q, want %q", got, want)
		}
	}
}

// TestValidateMethodNamesTheAdmittedSet: the refusal has to say which methods
// ARE admissible, because "not one an extension may declare" alone leaves the
// author guessing which of eight it should have been.
func TestValidateMethodNamesTheAdmittedSet(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		if err := validateMethod(method); err != nil {
			t.Errorf("validateMethod(%s) = %v, want nil", method, err)
		}
	}
	// HEAD may not be DECLARED, which is not the same as unreachable: Go's
	// ServeMux answers a HEAD request through every GET pattern, so a HEAD to a
	// declared GET runs the tool and discards the body. That is the stdlib's
	// behaviour and it is safe here precisely because a GET is read-scoped by
	// validateMethodAuthority — what this refusal prevents is a SECOND, separately
	// governed operation published on a method whose response nothing can carry.
	for _, method := range []string{http.MethodHead, http.MethodOptions, "post", ""} {
		if err := validateMethod(method); err == nil {
			t.Errorf("validateMethod(%q) = nil, want a refusal", method)
		}
	}
	err := validateMethod(http.MethodHead)
	for _, want := range []string{"get", "post", "put", "patch", "delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestCarriesBodySplitsTheMethodsTheSeamReadsABodyFrom: the serving seam and the
// declaration rules both branch on this, so it is pinned rather than inferred —
// a method that moved sides silently would be a route reading a body nothing
// sent, or ignoring arguments a client did send.
func TestCarriesBodySplitsTheMethodsTheSeamReadsABodyFrom(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		if !CarriesBody(method) {
			t.Errorf("CarriesBody(%s) = false, want true", method)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodHead} {
		if CarriesBody(method) {
			t.Errorf("CarriesBody(%s) = true, want false", method)
		}
	}
}

// TestABodylessMethodAcceptsFlatPrimitiveArguments: the shape the query decode
// can honestly serve. Asserted as an ACCEPTANCE because the refusals below could
// otherwise be tightened into "a bodyless method takes no arguments at all" and
// every refusal test would still pass.
func TestABodylessMethodAcceptsFlatPrimitiveArguments(t *testing.T) {
	read := wellFormed()
	read.Method = http.MethodGet
	read.RequestedScope = ScopeRead
	read.RbacObject, read.RbacAction = "", ""
	read.InputSchema = json.RawMessage(
		`{"type":"object","properties":{"payload":{"type":"string"},"limit":{"type":"integer"},` +
			`"ratio":{"type":"number"},"deep":{"type":"boolean"}},"additionalProperties":false}`)
	if err := read.Validate(); err != nil {
		t.Fatalf("a GET read taking flat primitives must validate: %v", err)
	}
	// And with no arguments at all — the common bodyless case (a list, a status
	// probe), which needs no query string.
	bare := read
	bare.InputSchema = nil
	if err := bare.Validate(); err != nil {
		t.Fatalf("a GET read taking no arguments must validate: %v", err)
	}
	// A DELETE is bodyless too, and mutating, so it keeps its RBAC pair.
	del := wellFormed()
	del.Method = http.MethodDelete
	del.RequestedScope = ScopeWrite
	del.RbacAction = RbacDelete
	del.InputSchema = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
	if err := del.Validate(); err != nil {
		t.Fatalf("a DELETE write taking a flat id must validate: %v", err)
	}
}
