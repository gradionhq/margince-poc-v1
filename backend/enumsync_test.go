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
	"lead.status":                    {"internal/modules/people", "LeadStatus"},
	"deal.status":                    {"internal/modules/deals", "DealStatus"},
	"stage.semantic":                 {"internal/modules/deals", "StageSemantic"},
	"person_consent.state":           {"internal/modules/consent", "ConsentState"},
	"offer_line_item.proposal_state": {"internal/modules/deals", "ProposalState"},
}

// checkInList captures CHECK (col IN ('a','b',…)) allowing an optional
// "col IS NULL OR" prefix; applied to a table block's accumulated text.
var checkInList = regexp.MustCompile(`(?is)CHECK\s*\(\s*(?:([a-z_]+)\s+IS\s+NULL\s+OR\s+)?([a-z_]+)\s+IN\s*\(([^)]*)\)`)

// alterTableStmt keys an ALTER statement's CHECK lists to their table.
var alterTableStmt = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+([a-z_]+)`)

// singleQuoted pulls the 'value' literals out of a captured IN-list.
var singleQuoted = regexp.MustCompile(`'([^']*)'`)

// migrationPrefix is the fixed-width name shape last-wins ordering needs.
var migrationPrefix = regexp.MustCompile(`^\d{4}_`)

// recordChecks derives every CHECK (col IN (…)) set in text and records
// it under table.col — the one spelling shared by the CREATE-block and
// ALTER-statement passes.
func recordChecks(sets map[string][]string, table, text string) {
	for _, m := range checkInList.FindAllStringSubmatch(text, -1) {
		var vals []string
		for _, q := range singleQuoted.FindAllStringSubmatch(m[3], -1) {
			vals = append(vals, q[1])
		}
		sort.Strings(vals)
		sets[table+"."+m[2]] = vals
	}
}

// tableCheckSets derives table.column → allowed set from the migration
// sources, using the same line-based CREATE TABLE scan as updateguard
// (column definitions nest parens beyond what a block regex can pair),
// plus a per-statement ALTER TABLE pass: vocabularies grow additively
// (drop CHECK + re-add wider), so the LAST migration to state a column's
// IN-list wins — walk order is lexical, which is migration order.
func tableCheckSets(t *testing.T) map[string][]string {
	t.Helper()
	sets := map[string][]string{}
	for _, root := range []string{"migrations/core", "migrations/custom"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
				return err
			}
			// Last-wins depends on lexical order being migration order:
			// every name must carry the fixed-width numeric prefix.
			if !migrationPrefix.MatchString(d.Name()) {
				return fmt.Errorf("%s: migration name lacks the 4-digit prefix the lexical-order derivation relies on", path)
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
				recordChecks(sets, current, block.String())
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
			for _, stmt := range strings.Split(string(raw), ";") {
				if alter := alterTableStmt.FindStringSubmatch(stmt); alter != nil {
					recordChecks(sets, alter[1], stmt)
				}
			}
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
