// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The declaration gates for the upload ceiling table.
//
// What they exist to catch, stated as the failure rather than the rule: a route
// that carries a file but is absent from the table parses under the 1 MiB JSON
// bound, so the ceiling it was granted never runs and every upload past a
// megabyte is refused for a reason nothing explains.
//
// The gate this replaces compared COUNTS — `len(parsers) != len(routes)` — and
// therefore could not see the likeliest version of that mistake: a mistyped
// path in the table leaves the real route undeclared while the totals still
// agree. So both gates below compare SETS, and both are built from a source
// nobody edits while adding a route: the contract for one, the tree for the
// other.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractOperation is the slice of one OpenAPI operation these gates read:
// which operation it is, and whether its body is a file.
//
//nolint:tagliatelle // these are OpenAPI's OWN field names, and they are camelCase; a snake_case tag here would decode a document that does not exist.
type contractOperation struct {
	OperationID string `yaml:"operationId"`
	RequestBody *struct {
		// Ref catches a requestBody this reader cannot see through. It is
		// refused rather than skipped: a body it cannot read is a body it cannot
		// prove is not a file, and skipping would drop the route out of the
		// obligation set in silence — the exact shape of the bug being gated.
		Ref     string              `yaml:"$ref"`
		Content map[string]struct{} `yaml:"content"`
	} `yaml:"requestBody"`
}

type contractDoc struct {
	// Each path item, method-keyed and left unparsed: a path item also carries
	// `parameters`, `summary` and friends, which are not operations.
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

const multipartMediaType = "multipart/form-data"

// httpMethods is every method an OpenAPI path item may carry an operation
// under. ALL of them, not just post: a multipart PUT is just as much an upload
// route, and a harvest that only looked at POST would leave it out of the
// obligation set while the chassis — which grants on POST alone — could not
// give it a ceiling either. Both halves would be silent.
var httpMethods = []string{
	"get", "put", "post", "patch", "delete", "head", "options", "trace",
}

// uploadOperationsIn reads a contract document for every operation that
// declares a file body, and reports what it could not account for.
//
// Split from the test so the HARVEST can be armed too. Arming only the set
// comparison leaves the reader half unproven, and a harvest that quietly
// returns nothing makes every comparison downstream pass.
func uploadOperationsIn(raw []byte) (map[string]string, []string, error) {
	var doc contractDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing the contract: %w", err)
	}
	ops := map[string]string{}
	var problems []string
	for path, item := range doc.Paths {
		for _, method := range httpMethods {
			node, present := item[method]
			if !present {
				continue
			}
			var op contractOperation
			if err := node.Decode(&op); err != nil {
				problems = append(problems, fmt.Sprintf(
					"%s %s could not be read: %v", strings.ToUpper(method), path, err))
				continue
			}
			if op.RequestBody == nil {
				continue
			}
			if op.RequestBody.Ref != "" {
				problems = append(problems, fmt.Sprintf(
					"%s %s takes its requestBody by $ref (%s), which this gate cannot "+
						"follow — it cannot tell whether the route carries a file",
					strings.ToUpper(method), path, op.RequestBody.Ref))
				continue
			}
			if _, isFile := op.RequestBody.Content[multipartMediaType]; !isFile {
				continue
			}
			if method != "post" {
				problems = append(problems, fmt.Sprintf(
					"%s %s takes a file body, but the chassis grants the wider ceiling "+
						"on POST alone, so this route can never receive one",
					strings.ToUpper(method), path))
				continue
			}
			// As the chassis sees it: the generated router mounts the contract's
			// paths under /v1, and the ceiling is chosen on the served path.
			ops["/v1"+path] = op.OperationID
		}
	}
	return ops, problems, nil
}

// uploadOperations is the OBLIGATION side of both gates: the contract is where
// an upload route comes into existence, so it is the only place that cannot be
// forgotten while adding one.
func uploadOperations(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "crm.yaml"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	ops, problems, err := uploadOperationsIn(raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(ops) == 0 {
		t.Fatal("the contract declares no file uploads at all — this gate is " +
			"reading the wrong document, and a gate that finds nothing passes " +
			"for the wrong reason")
	}
	return ops
}

// TestTheContractHarvestSeesWhatItCannotAccountFor arms the reader half.
//
// The version this replaces looked only at `post` and only at an inline
// requestBody, so a multipart PUT and a $ref'd body each dropped out of the
// obligation set without a word — and every set comparison downstream then
// agreed that nothing was missing.
func TestTheContractHarvestSeesWhatItCannotAccountFor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
		wants    string
	}{
		{
			name: "an inline multipart POST is harvested",
			document: `paths:
  /widgets:
    post:
      operationId: uploadWidget
      requestBody:
        content:
          multipart/form-data: {}
`,
			wants: "",
		},
		{
			name: "a multipart body on another method is reported",
			document: `paths:
  /widgets:
    put:
      operationId: replaceWidget
      requestBody:
        content:
          multipart/form-data: {}
`,
			wants: "PUT /widgets",
		},
		{
			name: "a requestBody this reader cannot follow is reported",
			document: `paths:
  /widgets:
    post:
      operationId: uploadWidget
      requestBody:
        $ref: '#/components/requestBodies/WidgetUpload'
`,
			wants: "$ref",
		},
		{
			name: "a path item's non-operation keys are not mistaken for one",
			document: `paths:
  /widgets:
    parameters:
      - name: id
        in: query
    post:
      operationId: uploadWidget
      requestBody:
        content:
          multipart/form-data: {}
`,
			wants: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, problems, err := uploadOperationsIn([]byte(tc.document))
			if err != nil {
				t.Fatalf("reading the fixture: %v", err)
			}
			if tc.wants == "" {
				if len(problems) != 0 {
					t.Fatalf("a readable document was reported as a problem: %v", problems)
				}
				if _, found := ops["/v1/widgets"]; !found {
					t.Errorf("the upload was not harvested at all: %v", ops)
				}
				return
			}
			if !slices.ContainsFunc(problems, func(p string) bool {
				return strings.Contains(p, tc.wants)
			}) {
				t.Errorf("nothing was reported about %q — it would drop out of the "+
					"obligation set in silence: %v", tc.wants, problems)
			}
		})
	}
}

// TestEveryUploadRouteIsDeclared is the gate proper: the routes that carry
// files and the routes with a ceiling are the same set, in both directions.
//
// Both directions matter and for different reasons. A contract route missing
// from the table runs at the JSON bound. A table entry with no contract route
// is a path nothing serves — usually the typo half of the first failure, and
// silent on its own.
func TestEveryUploadRouteIsDeclared(t *testing.T) {
	declared := uploadCeilings(testLimits)
	for _, problem := range censusProblems(uploadOperations(t), declared) {
		t.Error(problem)
	}
}

// TestTheDeclarationGateCanActuallyFail arms the gate above by handing its
// comparison the two mistakes it exists to catch.
//
// Written because the census this replaces was green against the bug it
// described: a guard whose failure has never been observed is a guard nobody
// has checked. The typo case is the important one — it is the shape a real
// mistake takes, and the count-based gate could not see it at all.
func TestTheDeclarationGateCanActuallyFail(t *testing.T) {
	contract := map[string]string{
		"/v1/attachments":     "uploadAttachment",
		"/v1/imports/sources": "uploadImportSource",
	}
	for _, tc := range []struct {
		name     string
		declared map[string]int64
		wants    string
	}{
		{
			name:     "a mistyped path leaves the real route undeclared",
			declared: map[string]int64{"/v1/attachment": 1, "/v1/imports/sources": 1},
			wants:    "/v1/attachments",
		},
		{
			name:     "a route the contract does not serve is declared anyway",
			declared: map[string]int64{"/v1/attachments": 1, "/v1/imports/sources": 1, "/v1/ghost": 1},
			wants:    "/v1/ghost",
		},
		{
			name:     "an upload route is simply missing",
			declared: map[string]int64{"/v1/attachments": 1},
			wants:    "/v1/imports/sources",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := censusProblems(contract, tc.declared)
			if len(problems) == 0 {
				t.Fatalf("the census passed over %q — it cannot catch the "+
					"mistake it is here for", tc.wants)
			}
			if !slices.ContainsFunc(problems, func(p string) bool {
				return strings.Contains(p, tc.wants)
			}) {
				t.Errorf("the census complained, but never about %q: %v", tc.wants, problems)
			}
		})
	}
}

// censusProblems reports every disagreement between the routes that carry files
// and the routes that have a ceiling. Separated from the test so the gate's own
// failure modes can be exercised without a contract that has the bug in it.
func censusProblems(contract map[string]string, declared map[string]int64) []string {
	var problems []string
	for path, op := range contract {
		if _, ok := declared[path]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s (%s) takes a file body but has no ceiling, so it parses "+
					"under the JSON bound and the cap it declares never runs",
				path, op))
		}
	}
	for path := range declared {
		if _, ok := contract[path]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s has a ceiling but no contract operation takes a file body "+
					"there — usually the other half of a mistyped path", path))
		}
	}
	slices.Sort(problems)
	return problems
}

// TestEveryMultipartParseNamesItsRoute is the tree-side gate: every place in
// the product that parses a file body says WHICH route it is parsing for, and
// those routes are exactly the ones with a ceiling.
//
// It counts parse sites against markers per FILE and compares the marker set
// against the declared routes, rather than asking which package a parse lives
// in. Package membership was the first version of this and it had a hole big
// enough to drive the original bug through: `internal/compose` already serves a
// declared upload operation, so a second, undeclared multipart parse added
// anywhere in that package answered "yes, this package serves an upload" and
// rode the 1 MiB bound in silence.
//
// A marker is not documentation — it is the parse site's half of a two-sided
// obligation, and the other side is derived from the contract.
func TestEveryMultipartParseNamesItsRoute(t *testing.T) {
	sites, markers := parseSitesAndMarkers(t)
	if len(sites) == 0 {
		t.Fatal("found no multipart parse anywhere — the walk is broken, and a " +
			"walk that finds nothing passes this test for the wrong reason")
	}
	for file, count := range sites {
		if got := len(markers[file]); got != count {
			t.Errorf("%s parses a multipart body %d time(s) but names a route %d "+
				"time(s) — an unnamed parse rides the JSON bound, so whatever cap "+
				"it declares is dead code", file, count, got)
		}
	}
	named := map[string]bool{}
	for _, routes := range markers {
		for _, route := range routes {
			named[route] = true
		}
	}
	declared := uploadCeilings(testLimits)
	for route := range named {
		if _, ok := declared[route]; !ok {
			t.Errorf("a parse names route %q, which has no ceiling — it will be "+
				"read under the JSON bound", route)
		}
	}
	for route := range declared {
		if !named[route] {
			t.Errorf("route %q has a ceiling but nothing parses a file body for "+
				"it — either the grant is stale or the parse lost its marker", route)
		}
	}
}

// uploadRouteMarker is the comment a parse site carries to name its route.
var uploadRouteMarker = regexp.MustCompile(`// upload:route (\S+)`)

// parseSitesAndMarkers walks the hand-written tree and returns, per file, how
// many multipart parses it performs and which routes it names.
func parseSitesAndMarkers(t *testing.T) (map[string]int, map[string][]string) {
	t.Helper()
	sites := map[string]int{}
	markers := map[string][]string{}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if n := strings.Count(string(source), "ParseMultipartForm("); n > 0 {
			sites[path] = n
		}
		for _, match := range uploadRouteMarker.FindAllStringSubmatch(string(source), -1) {
			markers[path] = append(markers[path], match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend tree: %v", err)
	}
	return sites, markers
}
