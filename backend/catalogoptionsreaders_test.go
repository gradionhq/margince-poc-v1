// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Who may read a custom field's OPTIONS.
//
// fieldcatalog.Column carries three things with two different disclosure rules
// (the port's own doc states them): Name and Type are schema, ambient to any
// caller who may read a record carrying the column, while Options is catalogue
// CONTENT — the values an admin authored — and a consumer handing them to a
// caller needs `custom_field:read`.
//
// A Column cannot enforce that. It holds no context, so the obligation lands on
// each consumer, and an obligation spread across consumers is one a new consumer
// meets by not knowing about it. That is how this arrived: the filter vocabulary
// began reporting options and disclosed them to any principal with `list:read`
// for two pushes before a review noticed.
//
// So this gate does exactly one thing, and it is worth saying what it is NOT.
// It does NOT verify that a reader checks the grant — a fitness function can see
// which package reads a field, never whether the grant it checked governs the
// value it returned. What it does is make a new reader VISIBLE: adding one fails
// here, and the fix is to add the package below, which is a line away from the
// rule in the port's doc. A gate that claimed the stronger thing would be a name
// promising more than its body, which is worse than this.
//
// Derived structurally rather than by name, in the shape jobregistrationban_test
// uses: a file cannot hold a Column without importing the port, so the readers
// are the files that import it AND select `.Options`. The one blind spot is a
// file that imports the port and separately selects `.Options` on some other
// type — it would be reported as a reader it is not. That is a false positive
// resolved by the same edit as a true one, and the alternative is type-checking
// the whole module to sharpen a two-entry list.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The port whose Column type this is about. Only this module can import it —
// it is internal, and each extension is its own module — so the walk is
// backend/ alone rather than the license gate's three trees.
const fieldcatalogPort = "github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"

// gatekit:fixture the reader set this gate asserts, not a waived cost — a
// package listed here is claimed to gate the read, and an entry no file reaches
// is reported stale below rather than quietly ratifying nothing.
//
// The packages that may read Options, each for a stated reason. Adding one is
// the point of the gate, not a workaround for it: state why the values reach a
// caller and which grant that caller had to hold.
var catalogOptionsReaders = map[string]string{
	// Owns the column. It writes the jsonb (marshalOptions) and reads it back
	// (unmarshalOptions), and its own list surface is `custom_field:read`.
	"internal/modules/customfields": "the catalogue itself",
	// Reports a picklist's values so a builder can offer them, and gates that
	// half on `custom_field:read` (customPicklistValuesAreReadable).
	"internal/modules/collections": "the filter vocabulary",
}

func TestOnlyTheDeclaredPackagesReadACustomFieldsOptions(t *testing.T) {
	found := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// path != "." because the root's own name is dotted and skipping it
			// would take the whole tree, reporting nothing for the most
			// reassuring possible reason.
			if path != "." && skipOptionsReaderDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		if readsCatalogOptions(t, path) {
			found[filepath.ToSlash(filepath.Dir(path))] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}

	if len(found) == 0 {
		t.Fatal("no reader of Column.Options found anywhere, so this gate has stopped reading the code it derives from")
	}
	for pkg := range found {
		if _, declared := catalogOptionsReaders[pkg]; !declared {
			t.Errorf("%s reads a custom field's Options and is not declared as a reader. Those values are catalogue CONTENT, not schema: a caller may only be told them if they hold custom_field:read (see the disclosure rule on fieldcatalog.Column). If this package gates that read, add it to catalogOptionsReaders with the reason; if it does not, gate it first.", pkg)
		}
	}
	for pkg, why := range catalogOptionsReaders {
		if !found[pkg] {
			t.Errorf("catalogOptionsReaders declares %s (%s) and it no longer reads Options — a stale entry makes this gate quietly permissive for whoever inherits the name", pkg, why)
		}
	}
}

// readsCatalogOptions answers whether one file both imports the port and selects
// `.Options`. Both halves are needed: the import is what makes a Column
// reachable, and the selector is the read.
func readsCatalogOptions(t *testing.T, path string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	imported := false
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) == fieldcatalogPort {
			imported = true
			break
		}
	}
	if !imported {
		return false
	}
	reads := false
	ast.Inspect(file, func(n ast.Node) bool {
		if selector, ok := n.(*ast.SelectorExpr); ok && selector.Sel.Name == "Options" {
			reads = true
		}
		return !reads
	})
	return reads
}

func skipOptionsReaderDir(name string) bool {
	return name == "node_modules" || name == "build" || name == "testdata" ||
		strings.HasPrefix(name, ".")
}
