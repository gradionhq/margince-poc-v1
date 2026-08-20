// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// jobs.Config.TestOnly and compose.JobRunnerConfig.TestOnly carry River's flag
// of the same name, which disables machinery that is "useful in production, but
// which may be harmful to tests" — in the pinned river@v0.43.0, the maintenance
// services' staggered startup. A test harness that boots a runner per test pays
// that stagger per test, which is what the flag buys back.
//
// The risk it introduces is one-directional and quiet: a production role that
// set it would run its queue maintainer without the jitter River added on
// purpose, and nothing would fail. There is no failure to observe — the workers
// still work, the jobs still run, and the only difference is that every
// maintenance service in a fleet wakes at once. So the gate cannot be a
// behavioural test; it has to be a census of who sets it.
//
// The field is a bool with a production-safe zero value, so this gate reads
// SETTERS rather than types. It reads one kind of setter only: the one that
// ORIGINATES a value. `TestOnly: cfg.TestOnly` forwards whatever the caller
// chose and is how the flag reaches River through two config structs — flagging
// it would flag the plumbing and leave nowhere to put the field. `TestOnly:
// true`, or any other computed value, is a decision, and a decision belongs in
// a test file.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// testOnlySetterFloor guards against a vacuous pass. If the walk stops finding
// the settings this gate knows about — jobtest's, and the suites that boot their
// own runner — then it has stopped reading the tree rather than started
// approving of it, and a production setter added the same day would pass
// unnoticed. The number is a floor, not a count: new test suites raise it.
const testOnlySetterFloor = 1

// TestJobRunnerConfigIsNeverSetInProduction is the whole obligation: TestOnly may
// be set from a test file, and from nowhere else.
func TestJobRunnerConfigIsNeverSetInProduction(t *testing.T) {
	var offenders, permitted []string
	fset := token.NewFileSet()

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		path = filepath.ToSlash(path)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		// A _test.go file is a test file whatever build tag it carries, which is
		// the same membership rule TestIntegrationSuitesMigrateOncePerProcess
		// walks by. jobtest/ is the exception that is not a _test.go file: it is
		// a test HARNESS package, imported only by integration suites and behind
		// the integration tag, so it is permitted by name rather than by suffix.
		isTest := strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "/integration/jobtest/") ||
			strings.Contains(path, "/integration/apptest/")

		ast.Inspect(file, func(n ast.Node) bool {
			if !originatesTestOnly(n) {
				return true
			}
			where := fset.Position(n.Pos()).String()
			if isTest {
				permitted = append(permitted, where)
				return true
			}
			offenders = append(offenders, where)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend module: %v", err)
	}

	if len(permitted) < testOnlySetterFloor {
		t.Fatalf("found %d TestOnly setters in test files, want at least %d — the walk has stopped "+
			"reading the tree, so a production setter would pass this gate unnoticed",
			len(permitted), testOnlySetterFloor)
	}
	if len(offenders) > 0 {
		t.Errorf("TestOnly is set outside a test file, which would run a production queue maintainer "+
			"without the staggered startup River adds on purpose:\n  %s\nSet it from a test harness only.",
			strings.Join(offenders, "\n  "))
	}
}

// originatesTestOnly reports whether n decides a TestOnly value, in either
// spelling — `TestOnly: x` in a composite literal, or `y.TestOnly = x`. A value
// that is itself somebody's TestOnly field is a forward, not a decision, and
// does not count: that is how one config struct hands the flag to the next.
func originatesTestOnly(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.KeyValueExpr:
		key, ok := node.Key.(*ast.Ident)
		return ok && key.Name == "TestOnly" && !forwardsTestOnly(node.Value)
	case *ast.AssignStmt:
		for i, lhs := range node.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "TestOnly" {
				continue
			}
			// A one-to-one assignment has a value to inspect; anything else
			// (a multi-value call) cannot be read as a forward, so it counts.
			if len(node.Rhs) == len(node.Lhs) && forwardsTestOnly(node.Rhs[i]) {
				continue
			}
			return true
		}
	}
	return false
}

// forwardsTestOnly reports whether expr is somebody else's TestOnly field.
func forwardsTestOnly(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "TestOnly"
}
