// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A version pin is only real if the pinned table's version actually moves.
//
// Membership in approvals.versionTables is what makes the binding real:
// resolveTargetVersion reads the row's version into the staged approval,
// validateRedemptionTarget re-checks it at redemption, and the agent gate
// forwards it as the released call's If-Match. A table whose version never
// changes gives a pin that re-checks a constant — three steps that all appear
// to work while binding the human's approval to nothing.
//
// The version moves by DATABASE TRIGGER: set_updated_at_bump_version(), the one
// house function that assigns version = OLD.version + 1, attached BEFORE UPDATE
// per table. So the obligation is derived from the migrations, which are the
// authority on what the database does, and not from any module's write shape —
// a table whose writers issue bare UPDATEs still bumps, because the trigger
// fires anyway.

import (
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

// bumpFunction is the sole mechanism that moves a version column: the only
// inline `version + 1` in the whole migration tree is this function's body.
const bumpFunction = "set_updated_at_bump_version"

// approvalsDir holds both halves of the derivation's Go side — the map and the
// constants its keys are spelled as.
const approvalsDir = "internal/modules/approvals"

// versionPinnedTableFloor is the vacuity floor on the Go side: the map's keys
// are constant identifiers declared in a different file of the same package, so
// a resolution that silently yields fewer subjects would certify the rest.
const versionPinnedTableFloor = 14

var (
	// triggerAttachment captures the table a CREATE TRIGGER fires on. Applied
	// per STATEMENT, never per line: the CREATE TRIGGER clause, the FOR EACH
	// ROW / WHEN clauses and the EXECUTE FUNCTION clause sit on different
	// lines, so a line-scoped match would find no attachment at all — a gate
	// reading green off zero subjects.
	triggerAttachment = regexp.MustCompile(`(?is)\bCREATE\s+TRIGGER\b.*?\bBEFORE\s+UPDATE\s+ON\s+([a-z_]+)`)

	// triggerDrop captures the table a DROP TRIGGER statement detaches from. It
	// does not name the function it dropped, which is exactly why it matters
	// here: this scan reads the migrations as a SET, so a drop it cannot order
	// against the attachment invalidates the conclusion for that table.
	triggerDrop = regexp.MustCompile(`(?is)\bDROP\s+TRIGGER\b[^;]*?\bON\s+([a-z_]+)`)

	// sqlLineComment strips `-- …` so prose naming the bump function is never
	// read as an attachment of it.
	sqlLineComment = regexp.MustCompile(`--[^\n]*`)
)

func TestEveryVersionPinnedTableBumpsItsVersion(t *testing.T) {
	pinned := versionPinnedTables(t)
	if len(pinned) < versionPinnedTableFloor {
		t.Fatalf("resolved only %d keys of approvals.versionTables, expected at least %d: the AST derivation broke, not the subject — every key is a constant identifier declared elsewhere in package %s, so this gate is currently certifying nothing",
			len(pinned), versionPinnedTableFloor, approvalsDir)
	}

	bumped, dropped := versionBumpAttachments(t)
	if len(bumped) == 0 {
		t.Fatalf("no %s attachment found in the migrations: the SQL derivation broke, not the subject — the scan must be statement-scoped, since the CREATE TRIGGER and EXECUTE FUNCTION clauses sit on different lines",
			bumpFunction)
	}
	versioned := versionedTables(t) // reused from updateguard_test.go

	for _, table := range sortedNames(pinned) {
		if at, ok := dropped[table]; ok {
			t.Errorf("approvals.versionTables pins %s, and %s drops a trigger on it: this derivation reads the migrations as a set and cannot tell whether the %s attachment survived the drop, so the pin can no longer be shown to bind anything. Re-attach %s BEFORE UPDATE on %s in a later migration, so one CREATE TRIGGER statement carries the attachment on its own, or drop %s from versionTables in %s/redeem.go and leave TargetVersion nil for that type",
				table, at, bumpFunction, bumpFunction, table, table, approvalsDir)
			continue
		}
		if _, ok := bumped[table]; ok {
			continue
		}
		column := "the table has no version column at all"
		if versioned[table] {
			column = "the table carries a version column that nothing moves"
		}
		t.Errorf("approvals.versionTables pins %s but no migration attaches %s BEFORE UPDATE on it — %s, so the staged pin, the redemption re-check and the released call's If-Match all compare a constant. Attach the trigger in a new migration under migrations/core, or drop %s from versionTables in %s/redeem.go and leave TargetVersion nil for that type",
			table, bumpFunction, column, table, approvalsDir)
	}
}

// versionPinnedTables resolves the table names in approvals.versionTables. The
// keys are constant identifiers, so the whole package is parsed — redeem.go
// alone resolves not a single one.
func versionPinnedTables(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files := parsePackageDir(t, fset, approvalsDir)
	consts := stringConsts(t, fset, files)

	lit := packageVarCompositeLit(t, files, "versionTables")
	tables := map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Errorf("approvals.versionTables at %s: element is not a key/value pair, so its table cannot be read", fset.Position(elt.Pos()))
			continue
		}
		switch key := kv.Key.(type) {
		case *ast.Ident:
			table, ok := consts[key.Name]
			if !ok {
				t.Errorf("approvals.versionTables at %s: key %s is not a string constant of package approvals, so its table cannot be read", fset.Position(key.Pos()), key.Name)
				continue
			}
			tables[table] = true
		case *ast.BasicLit:
			table, err := strconv.Unquote(key.Value)
			if err != nil {
				t.Errorf("approvals.versionTables at %s: key %s is not a quoted string", fset.Position(key.Pos()), key.Value)
				continue
			}
			tables[table] = true
		default:
			t.Errorf("approvals.versionTables at %s: key is neither an identifier nor a string literal, so its table cannot be read", fset.Position(kv.Key.Pos()))
		}
	}
	return tables
}

// versionBumpAttachments derives, per table, where the migrations attach the
// bump trigger and where they drop a trigger from it. Statement-scoped over
// both migration namespaces (ADR-0017), because the fork seam may attach its
// own.
func versionBumpAttachments(t *testing.T) (bumped, dropped map[string]string) {
	t.Helper()
	bumped, dropped = map[string]string{}, map[string]string{}
	for _, root := range []string{"migrations/core", "migrations/custom"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
				return err
			}
			raw, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.up.sql file from walking the trusted migrations tree
			if err != nil {
				return err
			}
			text := sqlLineComment.ReplaceAllString(string(raw), "")
			line := 1
			for _, stmt := range strings.Split(text, ";") {
				// Splitting on ";" leaves the newlines that FOLLOW the previous
				// statement at the head of this one. They belong to the gap, so
				// they are counted before the position is taken — counting them
				// afterwards reports every statement at the preceding ";".
				body := strings.TrimLeft(stmt, " \t\r\n")
				line += strings.Count(stmt[:len(stmt)-len(body)], "\n")
				at := path + ":" + strconv.Itoa(line)
				line += strings.Count(body, "\n")
				if m := triggerDrop.FindStringSubmatch(stmt); m != nil {
					dropped[m[1]] = at
					continue
				}
				if !strings.Contains(stmt, bumpFunction) {
					continue
				}
				if m := triggerAttachment.FindStringSubmatch(stmt); m != nil {
					bumped[m[1]] = at
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	return bumped, dropped
}

// parsePackageDir parses the hand-written Go files of ONE package directory,
// non-recursively: a subpackage's identifiers are a different scope and must
// not resolve here.
func parsePackageDir(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
	}
	return files
}

// stringConsts indexes every package-level string constant by name. A literal
// the parser accepted as token.STRING but that does not unquote is reported
// rather than skipped: the constant would silently drop out of the index, and
// every versionTables key spelled with it would then look unresolvable for a
// reason that has nothing to do with the pin.
func stringConsts(t *testing.T, fset *token.FileSet, files []*ast.File) map[string]string {
	t.Helper()
	consts := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, name := range vs.Names {
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Errorf("%s: const %s holds a string literal that does not unquote (%s): %v — it cannot enter the constant index, so any versionTables key spelled with it resolves to nothing",
							fset.Position(lit.Pos()), name.Name, lit.Value, err)
						continue
					}
					consts[name.Name] = value
				}
			}
		}
	}
	return consts
}

// packageVarCompositeLit returns the composite literal a named package-level
// var is initialised with.
func packageVarCompositeLit(t *testing.T, files []*ast.File, name string) *ast.CompositeLit {
	t.Helper()
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
					continue
				}
				lit, ok := vs.Values[0].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s is not initialised with a composite literal, so its subjects cannot be derived", name)
				}
				return lit
			}
		}
	}
	t.Fatalf("var %s not found in the parsed package: the derivation lost its subject list", name)
	return nil
}

// sortedNames gives a set's members a stable report order.
func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
