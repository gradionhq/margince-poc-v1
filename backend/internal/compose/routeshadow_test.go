// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// TestNoOperationPathIsShadowedByAnother derives the obligation from the
// contract itself: chi resolves a static path segment before a parameterized
// one, so a literal path that ALSO matches a templated sibling — under that
// sibling's parameter schema — makes the templated operation unreachable for
// the matching value. Such an operation is dead in the running server while
// looking perfectly alive in the contract, and nothing in the gates caught the
// one instance we shipped (the tests that "covered" it bypassed the mux).
//
// gorillamux's FindRoute only matches path SHAPE (segment count + static
// segments) — it has no notion of a path parameter's enum/format/pattern, so
// on its own it would flag GET /automations/catalog as shadowing
// GET /automations/{id} too (a uuid-format param structurally accepts any
// segment). Schema-awareness is layered on top here: a shape match is a real
// shadow only when the literal's differing segment(s) also satisfy the
// templated operation's OWN path-parameter schema — an enum containing
// "imap" accepts the literal "imap" segment; a uuid-format parameter never
// accepts "catalog".
func TestNoOperationPathIsShadowedByAnother(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}
	// Example-vs-schema fidelity is a separate obligation from routing shape;
	// disabling it here keeps this test's failure signal about shadowing, not
	// about an unrelated stale example elsewhere in a 12k-line contract.
	if err := doc.Validate(t.Context(), openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("contract does not validate: %v", err)
	}

	// gorillamux matches the FULL server URL (scheme + host + base path), not
	// just the path suffix — the contract's declared server carries a `/v1`
	// base path and an explicit host, and a request missing either never
	// matches ANY route, literal or templated, which would make this test
	// pass vacuously. baseURL anchors every synthetic request the same way a
	// real client would reach the server.
	baseURL := docServerBaseURL(t, doc)

	type route struct {
		method, path, op string
		item             *openapi3.PathItem
		operation        *openapi3.Operation
	}
	var literal, templated []route
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			r := route{method: method, path: path, op: op.OperationID, item: item, operation: op}
			if strings.Contains(path, "{") {
				templated = append(templated, r)
			} else {
				literal = append(literal, r)
			}
		}
	}

	for _, tmpl := range templated {
		single := openapi3.NewPaths(openapi3.WithPath(tmpl.path, tmpl.item))
		soloDoc := &openapi3.T{
			OpenAPI:    doc.OpenAPI,
			Info:       doc.Info,
			Paths:      single,
			Components: doc.Components,
			Servers:    doc.Servers,
		}
		router, err := gorillamux.NewRouter(soloDoc)
		if err != nil {
			t.Fatalf("building the single-route matcher for %s %s: %v", tmpl.method, tmpl.path, err)
		}
		pathParams := pathParameters(tmpl.item, tmpl.operation)

		for _, lit := range literal {
			if lit.method != tmpl.method {
				continue
			}
			req, err := http.NewRequest(lit.method, baseURL+lit.path, nil)
			if err != nil {
				t.Fatalf("building request for %s %s: %v", lit.method, lit.path, err)
			}
			_, vars, err := router.FindRoute(req)
			if err != nil {
				continue // different shape entirely — not a candidate shadow
			}
			if !allParamsAccept(pathParams, vars) {
				continue // shape matches, but the literal's value(s) fail the templated route's own schema — e.g. catalog vs a uuid {id}
			}
			t.Errorf(
				"operation %q (%s %s) is unreachable for the value that makes %q (%s %s) match: chi resolves the literal route first",
				tmpl.op, tmpl.method, tmpl.path, lit.op, lit.method, lit.path,
			)
		}
	}
}

// pathParameters resolves the effective `in: path` parameter set for an
// operation: path-item-level parameters (the common case — a shared `{$ref:
// .../CaptureProvider}` declared once on the path) overridden by any
// operation-level parameter of the same name.
func pathParameters(item *openapi3.PathItem, op *openapi3.Operation) map[string]*openapi3.Schema {
	out := map[string]*openapi3.Schema{}
	collect := func(params openapi3.Parameters) {
		for _, pRef := range params {
			p := pRef.Value
			if p == nil || p.In != openapi3.ParameterInPath || p.Schema == nil || p.Schema.Value == nil {
				continue
			}
			out[p.Name] = p.Schema.Value
		}
	}
	collect(item.Parameters)
	collect(op.Parameters)
	return out
}

// allParamsAccept reports whether every path-parameter value gorillamux
// extracted for a shape-matched request also satisfies that parameter's own
// schema (enum / format / pattern) — the discriminator between a genuine
// shadow (imap ∈ the {provider} enum) and a benign shape coincidence
// (catalog is not a valid uuid).
func allParamsAccept(schemas map[string]*openapi3.Schema, vars map[string]string) bool {
	for name, value := range vars {
		schema, ok := schemas[name]
		if !ok {
			continue // gorillamux var with no declared path parameter — nothing to check
		}
		if !schemaAcceptsPathValue(schema, value) {
			return false
		}
	}
	return true
}

// uuidPattern mirrors openapi3.FormatOfStringForUUIDOfRFC4122 — the contract
// declares `format: uuid` on every id-shaped path parameter, but that format
// has no built-in validator registered in kin-openapi's openapi3 package
// (only byte/date/date-time/ipv4/ipv6 are registered by default), so the
// check is inlined rather than depending on validator registration order.
var uuidPattern = regexp.MustCompile(openapi3.FormatOfStringForUUIDOfRFC4122)

// schemaAcceptsPathValue checks the constraints that actually distinguish a
// real collision from a benign one for the path parameters this contract
// uses: a closed `enum` (the CaptureProvider case) and `format: uuid` (every
// `{id}` parameter). A parameter with neither constraint places no
// discriminating obligation on the segment.
func schemaAcceptsPathValue(schema *openapi3.Schema, value string) bool {
	if len(schema.Enum) > 0 {
		for _, e := range schema.Enum {
			if s, ok := e.(string); ok && s == value {
				return true
			}
		}
		return false
	}
	if schema.Format == "uuid" {
		return uuidPattern.MatchString(value)
	}
	if schema.Pattern != "" {
		re, err := regexp.Compile(schema.Pattern)
		if err != nil {
			return true // an uncompilable pattern is a contract-authoring defect, not this test's concern
		}
		return re.MatchString(value)
	}
	return true
}

// docServerBaseURL is the contract's declared server URL (scheme + host +
// base path, e.g. `https://crm.example.com/v1`) — gorillamux's matcher
// requires a request to carry all three, so every synthetic request in this
// file is built against it rather than a bare path.
func docServerBaseURL(t *testing.T, doc *openapi3.T) string {
	t.Helper()
	if len(doc.Servers) == 0 || doc.Servers[0].URL == "" {
		t.Fatal("the contract declares no server URL to route synthetic requests against")
	}
	return strings.TrimRight(doc.Servers[0].URL, "/")
}

// TestRouteShadowMatcherFlagsAnEnumCollisionButNotAUUIDOne is a
// characterization test of the matcher's schema-awareness, so the main
// test's verdict can be trusted. It loads the same contract and asserts the
// two reference cases directly: an enum path parameter containing "imap"
// accepts the literal "/connectors/imap/connect" segment, while a
// uuid-format path parameter never accepts the literal "/automations/catalog"
// segment.
func TestRouteShadowMatcherFlagsAnEnumCollisionButNotAUUIDOne(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}
	if err := doc.Validate(t.Context(), openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("contract does not validate: %v", err)
	}
	baseURL := docServerBaseURL(t, doc)

	shapeMatches := func(t *testing.T, path, method, literalPath string) (map[string]string, bool) {
		t.Helper()
		item := doc.Paths.Find(path)
		if item == nil {
			t.Fatalf("path %q not found in the contract", path)
		}
		soloDoc := &openapi3.T{
			OpenAPI:    doc.OpenAPI,
			Info:       doc.Info,
			Paths:      openapi3.NewPaths(openapi3.WithPath(path, item)),
			Components: doc.Components,
			Servers:    doc.Servers,
		}
		router, err := gorillamux.NewRouter(soloDoc)
		if err != nil {
			t.Fatalf("building the matcher for %q: %v", path, err)
		}
		req, err := http.NewRequest(method, baseURL+literalPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, vars, err := router.FindRoute(req)
		if err != nil {
			return nil, false
		}
		op := item.Operations()[method]
		return vars, allParamsAccept(pathParameters(item, op), vars)
	}

	t.Run("enum collision: connectors/{provider}/connect accepts imap", func(t *testing.T) {
		_, accepted := shapeMatches(t, "/connectors/{provider}/connect", http.MethodPost, "/connectors/imap/connect")
		if !accepted {
			t.Fatal("expected {provider} (enum incl. imap) to accept the literal imap path, but it did not")
		}
	})

	t.Run("uuid collision: automations/{id} does NOT accept catalog", func(t *testing.T) {
		_, accepted := shapeMatches(t, "/automations/{id}", http.MethodGet, "/automations/catalog")
		if accepted {
			t.Fatal("expected {id} (format: uuid) NOT to accept the literal \"catalog\" path, but it did")
		}
	})
}
