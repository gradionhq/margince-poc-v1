// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// contractWith wraps a path item into the smallest document this reader will
// accept, so a case reads as the operation it is about.
func contractWith(pathItem string) []byte {
	return []byte("openapi: 3.0.3\npaths:\n" + pathItem)
}

// yogiOperation is a well-formed extension operation, indented for a paths
// mapping. Cases below substitute one line of it.
const yogiOperation = `  /ext/u/quote:
    post:
      operationId: uQuote
      x-mcp-tool:
        verb: u_quote
        version: 1.0.0
        title: A quote
        tier: auto_execute
        scope: read
        description: Return one quote and nothing else.
      requestBody:
        content:
          application/json:
            schema:
              type: object
      responses:
        '200':
          content:
            application/json:
              schema:
                type: object
                properties:
                  quote: {type: string}
`

func oneUnit() []extensionUnit { return []extensionUnit{{Name: "u"}} }

// TestExtensionVerbsReadsAnOperationOutOfTheMergedContract is the happy path:
// the whole declaration comes out of the document, including the schemas, which
// are taken from the operation's own requestBody/200 rather than from a
// duplicate copy under the annotation.
func TestExtensionVerbsReadsAnOperationOutOfTheMergedContract(t *testing.T) {
	got, err := verbsInContract("crm.yaml", oneUnit(), contractWith(yogiOperation))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d operations, want 1", len(got))
	}
	v := got[0].verb
	if v.Unit != "u" || v.Contract != "crm.yaml" || v.OperationID != "uQuote" {
		t.Fatalf("identity = %+v", v)
	}
	if v.Route != "/ext/u/quote" || v.Method != "POST" {
		t.Fatalf("surface = %s %s", v.Method, v.Route)
	}
	if v.Tool != "u_quote" || v.Tier != extension.TierAutoExecute || v.RequestedScope != extension.ScopeRead {
		t.Fatalf("governance = tool=%q tier=%q scope=%q", v.Tool, v.Tier, v.RequestedScope)
	}
	if v.Title != "A quote" || v.Description != "Return one quote and nothing else." || v.Version != "1.0.0" {
		t.Fatalf("prose = title=%q description=%q version=%q", v.Title, v.Description, v.Version)
	}
	if string(v.InputSchema) != `{"type":"object"}` {
		t.Fatalf("InputSchema = %s", v.InputSchema)
	}
	// JSON object keys are emitted sorted, so the literal is stable regardless
	// of the YAML's own key order.
	if string(v.OutputSchema) != `{"properties":{"quote":{"type":"string"}},"type":"object"}` {
		t.Fatalf("OutputSchema = %s", v.OutputSchema)
	}
	if got[0].fragmentHash == "" || len(got[0].fragmentHash) != 64 {
		t.Fatalf("fragmentHash = %q, want a sha256 hex digest", got[0].fragmentHash)
	}
}

// TestTheFragmentHashCoversWhatTheDescriptorDoesNot: the four governance fields
// are in the descriptor; everything else about the declaration is in the hash.
// A schema or a description change with no tier change must still move it, or
// an operator resolution recorded against the old text carries silently to new
// text a model will behave differently on.
func TestTheFragmentHashCoversWhatTheDescriptorDoesNot(t *testing.T) {
	baseline, err := verbsInContract("crm.yaml", oneUnit(), contractWith(yogiOperation))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string][2]string{
		"the description":     {"Return one quote and nothing else.", "Return one quote, and say where it came from."},
		"the title":           {"title: A quote", "title: A quotation"},
		"the response schema": {"quote: {type: string}", "quote: {type: string, maxLength: 200}"},
		"the request schema":  {"          application/json:\n            schema:\n              type: object", "          application/json:\n            schema:\n              type: object\n              properties:\n                seed: {type: integer}"},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := strings.Replace(yogiOperation, edit[0], edit[1], 1)
			if mutated == yogiOperation {
				t.Fatalf("the edit %q did not apply — this case checked nothing", edit[0])
			}
			got, err := verbsInContract("crm.yaml", oneUnit(), contractWith(mutated))
			if err != nil {
				t.Fatal(err)
			}
			if got[0].fragmentHash == baseline[0].fragmentHash {
				t.Fatalf("changing %s did not move the fragment hash", name)
			}
		})
	}

	// And it is stable across a re-read of the same bytes: a hash that moved on
	// every generation would make every manifest churn.
	again, err := verbsInContract("crm.yaml", oneUnit(), contractWith(yogiOperation))
	if err != nil {
		t.Fatal(err)
	}
	if again[0].fragmentHash != baseline[0].fragmentHash {
		t.Fatal("the fragment hash is not stable across two reads of the same bytes")
	}
}

// TestExtensionVerbRefusals: the reader is fail-closed, because every one of
// these would otherwise publish a route, an authority request, or a schema that
// disagrees with what the process serves.
func TestExtensionVerbRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		pathItem string
		wantErr  string
	}{
		"an operation with no x-mcp-tool": {
			pathItem: "  /ext/u/quote:\n    post:\n      operationId: uQuote\n",
			wantErr:  "declares no x-mcp-tool",
		},
		"an unknown key in the annotation": {
			pathItem: strings.Replace(yogiOperation, "        scope: read", "        scope: read\n        scopes: [read]", 1),
			wantErr:  "field scopes not found",
		},
		"a route no enabled unit owns": {
			pathItem: strings.ReplaceAll(yogiOperation, "/ext/u/quote", "/ext/other/quote"),
			wantErr:  "no enabled unit owns it",
		},
		"a path item with no operation": {
			pathItem: "  /ext/u/quote:\n    summary: nothing here\n",
			wantErr:  "declares no operation",
		},
		"a tier outside the vocabulary": {
			pathItem: strings.Replace(yogiOperation, "tier: auto_execute", "tier: dynamic", 1),
			wantErr:  "not one an extension may request",
		},
		"a scope outside the vocabulary": {
			pathItem: strings.Replace(yogiOperation, "scope: read", "scope: admin", 1),
			wantErr:  "not in the Passport scope vocabulary",
		},
		"a GET, which carries no body": {
			pathItem: strings.Replace(yogiOperation, "    post:", "    get:", 1),
			wantErr:  "is not one an extension operation may declare",
		},
		"no requestBody": {
			pathItem: "  /ext/u/quote:\n    post:\n      operationId: uQuote\n      x-mcp-tool:\n        verb: u_quote\n        version: 1.0.0\n        tier: auto_execute\n        scope: read\n        description: Does one thing.\n",
			wantErr:  "declares no requestBody",
		},
		"a requestBody with no JSON schema": {
			pathItem: strings.Replace(yogiOperation, "          application/json:\n            schema:\n              type: object", "          text/plain: {}", 1),
			wantErr:  "no application/json schema",
		},
		"a $ref request schema": {
			pathItem: strings.Replace(yogiOperation, "            schema:\n              type: object", "            schema: {$ref: '#/components/schemas/Thing'}", 1),
			wantErr:  "does not resolve references",
		},
		"a $ref response schema": {
			pathItem: strings.Replace(yogiOperation, "              schema:\n                type: object\n                properties:\n                  quote: {type: string}", "              schema: {$ref: '#/components/schemas/Thing'}", 1),
			wantErr:  "does not resolve references",
		},
		"a tool verb outside the grammar": {
			pathItem: strings.Replace(yogiOperation, "verb: u_quote", "verb: U-Quote", 1),
			wantErr:  "not a valid verb",
		},
		"a misspelled x-rbac-object": {
			// The worst failure mode in this reader if it were tolerated: the
			// object never registers, and a stored role document granting it
			// then fails the whole of that user's identity resolution.
			pathItem: strings.Replace(yogiOperation, "      operationId: uQuote", "      operationId: uQuote\n      x-rbac-objects: ext_u_widget", 1),
			wantErr:  "which this generator does not read",
		},
		"an x- annotation this tier does not act on": {
			pathItem: strings.Replace(yogiOperation, "      operationId: uQuote", "      operationId: uQuote\n      x-agent-access: human-only", 1),
			wantErr:  "which this generator does not read",
		},
		"an RBAC object outside the unit's namespace": {
			pathItem: strings.Replace(yogiOperation, "      operationId: uQuote", "      operationId: uQuote\n      x-rbac-object: ext_other_widget", 1),
			wantErr:  "outside extension",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verbsInContract("crm.yaml", oneUnit(), contractWith(tc.pathItem))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestARbacObjectInTheUnitsNamespaceIsRead: the refusal above only means
// something if the accepting path works.
func TestARbacObjectInTheUnitsNamespaceIsRead(t *testing.T) {
	pathItem := strings.Replace(yogiOperation, "      operationId: uQuote", "      operationId: uQuote\n      x-rbac-object: ext_u_widget", 1)
	got, err := verbsInContract("crm.yaml", oneUnit(), contractWith(pathItem))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].verb.RbacObject != "ext_u_widget" {
		t.Fatalf("RbacObject = %q", got[0].verb.RbacObject)
	}
}

// TestACoreRouteIsNotReadAsAnExtensionOperation: the base contract's own 300-odd
// routes must be skipped silently, or every generation would demand an
// extension annotation on the whole CRM surface.
func TestACoreRouteIsNotReadAsAnExtensionOperation(t *testing.T) {
	got, err := verbsInContract("crm.yaml", oneUnit(),
		contractWith("  /v1/deals:\n    get:\n      operationId: listDeals\n"+yogiOperation))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].verb.OperationID != "uQuote" {
		t.Fatalf("read %d operations, want only the extension one", len(got))
	}
}

// TestAContractWithNoPathsDeclaresNothing: jobs.yaml and ai-tasks.yaml carry
// kinds and tasks, not routes. Reading them must be a no-op rather than an
// error, because they are composed on every run.
func TestAContractWithNoPathsDeclaresNothing(t *testing.T) {
	got, err := verbsInContract("jobs.yaml", oneUnit(), []byte("kinds:\n  thing: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("read %d operations from a contract with no paths", len(got))
	}
	// A `paths:` that is present but is not a mapping is different: something
	// is there and cannot be read, which is worth naming.
	if _, err := verbsInContract("crm.yaml", oneUnit(), []byte("paths: [one, two]\n")); err == nil {
		t.Fatal("a non-mapping paths block was accepted")
	}
}

// TestExtensionVerbsIsOrderedAndCoversEveryContract: the result is emitted into
// generated Go and hashed into committed manifests, so its order must not depend
// on map iteration. Two units, two operations each, deliberately declared in
// reverse order.
func TestExtensionVerbsIsOrderedAndCoversEveryContract(t *testing.T) {
	crm := strings.ReplaceAll(yogiOperation, "/ext/u/quote", "/ext/zeta/quote")
	crm = strings.ReplaceAll(crm, "verb: u_quote", "verb: zeta_quote")
	crm = strings.ReplaceAll(crm, "operationId: uQuote", "operationId: zetaQuote")
	alpha := strings.ReplaceAll(yogiOperation, "/ext/u/quote", "/ext/alpha/quote")
	alpha = strings.ReplaceAll(alpha, "verb: u_quote", "verb: alpha_quote")
	alpha = strings.ReplaceAll(alpha, "operationId: uQuote", "operationId: alphaQuote")

	units := []extensionUnit{{Name: "zeta"}, {Name: "alpha"}}
	contracts := map[string][]byte{
		"crm.yaml":           contractWith(crm + alpha),
		"jobs.yaml":          []byte("kinds: {}\n"),
		"ai-tasks.yaml":      []byte("tasks: {}\n"),
		"public-events.yaml": []byte("paths: {}\n"),
	}
	got, err := extensionVerbs(units, contracts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("read %d operations, want %d", len(got), len(want))
	}
	for i, unit := range want {
		if string(got[i].verb.Unit) != unit {
			t.Errorf("operation %d is %s's, want %s's — the order follows map iteration", i, got[i].verb.Unit, unit)
		}
	}

	// A contract the composer produced but this reader was not handed is an
	// error, not an empty read: it would silently drop every operation in it.
	delete(contracts, "jobs.yaml")
	if _, err := extensionVerbs(units, contracts); err == nil {
		t.Fatal("a missing composed contract was accepted")
	}
}

// TestRouteUnitTakesTheWholeSegment: the unit is a path SEGMENT, so a route
// under /ext/alpha-two/ must not be attributed to a unit named "alpha".
func TestRouteUnitTakesTheWholeSegment(t *testing.T) {
	for route, want := range map[string]string{
		"/ext/alpha/quote":     "alpha",
		"/ext/alpha-two/quote": "alpha-two",
		"/ext/alpha":           "alpha",
		"/ext/alpha/a/b/c":     "alpha",
	} {
		if got := routeUnit(route); got != want {
			t.Errorf("routeUnit(%q) = %q, want %q", route, got, want)
		}
	}
}

// TestVerbsByUnitPreservesOrder: a unit's manifest lists its risk tiers in this
// order, so a grouping that reordered them would churn every committed manifest
// whenever another unit was enabled.
func TestVerbsByUnitPreservesOrder(t *testing.T) {
	verbs := []declaredVerb{
		{verb: extension.Verb{Unit: "alpha", Tool: "a_one"}},
		{verb: extension.Verb{Unit: "beta", Tool: "b_one"}},
		{verb: extension.Verb{Unit: "alpha", Tool: "a_two"}},
	}
	byUnit := verbsByUnit(verbs)
	if len(byUnit) != 2 {
		t.Fatalf("grouped into %d units, want 2", len(byUnit))
	}
	if got := byUnit["alpha"]; len(got) != 2 || got[0].verb.Tool != "a_one" || got[1].verb.Tool != "a_two" {
		t.Fatalf("alpha's operations = %v, want them in declaration order", got)
	}
	// A unit with no declared operation is absent rather than present-and-empty,
	// which is what lets a jurisdiction-only unit keep its `"risk_tiers": []`.
	if _, ok := byUnit["gamma"]; ok {
		t.Fatal("a unit declaring nothing was grouped")
	}
}
