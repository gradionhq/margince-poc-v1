// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The enum-vocabulary sync as a fitness function: where domain logic
// branches on a typed Go enum, its constant set must equal the schema's
// CHECK (col IN (...)) set for the column it mirrors. The valid set
// living only in the DB is how a typo'd Go literal compiles and
// misbehaves silently; a Go set drifting from the CHECK is how a valid
// value 500s at insert. This gate pins the two spellings together —
// registry-driven, so adding an enum means adding one line here, and
// the sets themselves are DERIVED from the migration sources and the
// Go const declarations, never restated.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// enumBindings maps "table.column" to the Go type that mirrors it.
var enumBindings = map[string]struct{ pkgDir, typeName string }{
	"lead.status":          {"internal/modules/people", "LeadStatus"},
	"deal.status":          {"internal/modules/deals", "DealStatus"},
	"stage.semantic":       {"internal/modules/deals", "StageSemantic"},
	"person_consent.state": {"internal/modules/consent", "ConsentState"},
}

// checkInList captures CHECK (col IN ('a','b',…)) allowing an optional
// "col IS NULL OR" prefix; applied to a table block's accumulated text.
var checkInList = regexp.MustCompile(`(?is)CHECK\s*\(\s*(?:([a-z_]+)\s+IS\s+NULL\s+OR\s+)?([a-z_]+)\s+IN\s*\(([^)]*)\)`)

// tableCheckSets derives table.column → allowed set from the migration
// sources, using the same line-based CREATE TABLE scan as updateguard
// (column definitions nest parens beyond what a block regex can pair).
func tableCheckSets(t *testing.T) map[string][]string {
	t.Helper()
	sets := map[string][]string{}
	for _, root := range []string{"migrations/core", "migrations/custom"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
				return err
			}
			raw, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.up.sql file from walking the trusted migrations tree
			if err != nil {
				return err
			}
			current, block := "", strings.Builder{}
			flush := func() {
				if current == "" {
					return
				}
				for _, m := range checkInList.FindAllStringSubmatch(block.String(), -1) {
					var vals []string
					for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[3], -1) {
						vals = append(vals, q[1])
					}
					sort.Strings(vals)
					sets[current+"."+m[2]] = vals
				}
				current = ""
				block.Reset()
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if m := createTableLine.FindStringSubmatch(line); m != nil {
					flush()
					current = m[1]
					continue
				}
				if strings.HasPrefix(line, ");") {
					flush()
					continue
				}
				if current != "" {
					block.WriteString(line)
					block.WriteString("\n")
				}
			}
			flush()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sets
}

// goConstSet derives the string values of every constant declared with
// the given type in the package directory.
func goConstSet(t *testing.T, pkgDir, typeName string) []string {
	t.Helper()
	var vals []string
	fset := token.NewFileSet()
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				id, ok := vs.Type.(*ast.Ident)
				if !ok || id.Name != typeName {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					vals = append(vals, s)
				}
			}
		}
	}
	sort.Strings(vals)
	return vals
}

func TestEveryDomainEnumMatchesItsSchemaCheck(t *testing.T) {
	checks := tableCheckSets(t)
	for col, binding := range enumBindings {
		want, ok := checks[col]
		if !ok {
			t.Errorf("enumBindings[%s]: no CHECK (… IN (…)) found in the migrations — stale binding or broken derivation", col)
			continue
		}
		got := goConstSet(t, binding.pkgDir, binding.typeName)
		if len(got) == 0 {
			t.Errorf("enumBindings[%s]: no %s constants found in %s", col, binding.typeName, binding.pkgDir)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: Go %s set %v != schema CHECK set %v — change both together", col, binding.typeName, got, want)
		}
	}
}
