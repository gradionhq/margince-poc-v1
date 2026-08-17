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
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractPaths is the slice of the OpenAPI document these gates read: which
// operation each path's POST is, and whether its body is a file.
//
//nolint:tagliatelle // these are OpenAPI's OWN field names, and they are camelCase; a snake_case tag here would decode a document that does not exist.
type contractPaths struct {
	Paths map[string]struct {
		Post *struct {
			OperationID string `yaml:"operationId"`
			RequestBody *struct {
				Content map[string]struct{} `yaml:"content"`
			} `yaml:"requestBody"`
		} `yaml:"post"`
	} `yaml:"paths"`
}

const multipartMediaType = "multipart/form-data"

// uploadOperations reads the contract for every POST that declares a file body.
// This is the OBLIGATION side of both gates: the contract is where an upload
// route comes into existence, so it is the only place that cannot be forgotten
// while adding one.
func uploadOperations(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "crm.yaml"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	var doc contractPaths
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	ops := map[string]string{}
	for path, item := range doc.Paths {
		if item.Post == nil || item.Post.RequestBody == nil {
			continue
		}
		if _, isFile := item.Post.RequestBody.Content[multipartMediaType]; isFile {
			// As the chassis sees it: the generated router mounts the contract's
			// paths under /v1, and the ceiling is chosen on the served path.
			ops["/v1"+path] = item.Post.OperationID
		}
	}
	if len(ops) == 0 {
		t.Fatal("the contract declares no file uploads at all — this gate is " +
			"reading the wrong document, and a gate that finds nothing passes " +
			"for the wrong reason")
	}
	return ops
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

// TestEveryMultipartParseServesADeclaredUploadRoute is the tree-side backstop:
// a handler can only reach a multipart parse if its package implements one of
// the contract's declared upload operations.
//
// It works by package rather than by file because the parse does not always sit
// in the handler — the CSV import reads its body in a helper beside it — and a
// gate that insisted on the same file would be describing today's layout rather
// than the obligation.
func TestEveryMultipartParseServesADeclaredUploadRoute(t *testing.T) {
	parsers := packagesMatching(t, "ParseMultipartForm(")
	if len(parsers) == 0 {
		t.Fatal("found no multipart parse anywhere — the walk is broken, and a " +
			"walk that finds nothing passes this test for the wrong reason")
	}
	servers := map[string]bool{}
	for _, op := range uploadOperations(t) {
		method := strings.ToUpper(op[:1]) + op[1:]
		for pkg := range packagesMatching(t, ") "+method+"(w http.ResponseWriter") {
			servers[pkg] = true
		}
	}
	for pkg := range parsers {
		if !servers[pkg] {
			t.Errorf("%s parses a multipart body but implements no declared "+
				"upload operation — whatever route it serves rides the JSON "+
				"bound, so its own cap is dead code", pkg)
		}
	}
	for pkg := range servers {
		if !parsers[pkg] {
			t.Errorf("%s implements a declared upload operation but nothing in "+
				"it parses a multipart body", pkg)
		}
	}
}

// packagesMatching returns every backend package holding a hand-written source
// file that contains the given literal.
func packagesMatching(t *testing.T, literal string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Generated files are skipped: the contract's router and its interface
		// restate every handler signature, so a generated package would answer
		// "implements this operation" for all of them while parsing nothing.
		if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(source), literal) {
			found[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend tree: %v", err)
	}
	return found
}
