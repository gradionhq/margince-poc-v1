// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A unit's SQL addresses the unit's own tables.
//
// The extension runtime hands a unit a workspace-pinned transaction on the
// SHARED application role, so its SQL can name any table that role can reach.
// pkg/extension/runtime.go says so in the open, under "WHAT IS NOT WALLED AT
// ALL": core's tables, another unit's tables, extension_secret. The fix for the
// wall itself is a per-unit database role (#628), and this is not it.
//
// WHAT THIS IS: defence against mistakes, and nothing else. It reads the SQL a
// unit's source spells out and refuses a table that is not the unit's own,
// which is worth doing BEFORE a unit grows the habit — no shipped unit names a
// core table today, so the narrowing costs nothing now and costs a rewrite
// later. A unit is trusted in-process code either way, and a table name the
// source does not spell out defeats a static reader by construction. Read it as
// a lint; the boundary is #628's.
//
// WHAT IT READS. Every .go file a unit ships, tests included: a unit's test that
// seeds a core table is the same mistake as its handler doing so, and it is
// where the habit would start. String CONSTANTS are folded first, because the
// one unit shipping SQL today spells every table through one — `"SELECT " +
// noteColumns + " FROM " + noteTable` is what a real statement looks like here,
// and a gate that cannot see through the concatenation would read green over
// the only SQL in the tree.
//
// A folded string is judged when it OPENS as a statement (`SELECT … FROM …`,
// `INSERT … INTO …`), so that prose which merely contains a keyword — "hello
// from the demo extension" — is not read as a query. Two consequences worth
// stating rather than discovering: a statement whose opening fragment is itself
// computed is not judged at all, and a table name that is computed IS a finding
// (name it with a string constant, and the gate can read it).
//
// The allowlist is the unit's namespace, not a list of core tables: a table is
// the unit's own when it is `ext.<namespace>_…` — derived through the same
// extension.Name(…).Namespace() the migration gate and the unit's database role
// come from, so the three cannot drift. A bare, unqualified name is refused too,
// and for the reason notes' own constant gives: the ext schema is on no
// search_path the app connects with, so `ext_notes_note` unqualified is a public
// table the unit does not own.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extSchema is the one schema an extension's tables live in (ADR-0069 §9); the
// migration gate refuses a unit relation anywhere else.
const extSchema = "ext"

// computedFragment stands in for a fragment the fold cannot read — a function
// call, a parameter, a `%s`. It is deliberately not identifier-shaped, so it
// tokenises on its own and a name it is glued to (`"public." + t`) still reads
// as the schema-qualified reference it is.
const computedFragment = "?"

// TestExtensionSQLNamesOnlyTheUnitsOwnTables reads every unit's SQL and refuses
// a table outside the unit's namespace.
func TestExtensionSQLNamesOnlyTheUnitsOwnTables(t *testing.T) {
	trees := extensionTrees(t)
	if len(trees) == 0 {
		t.Fatal("no extension tree was found: this gate judges extensions/* and fixtures/extensions/*, and a run that enrols none certifies nothing")
	}
	judged := 0
	for dir, unit := range trees {
		scan := scanUnitSQL(t, unit, goSources(t, dir))
		judged += scan.tables
		for _, finding := range scan.findings {
			t.Error(finding)
		}
	}
	// The anti-vacuity check. Every refusal above is a statement about SQL the
	// gate read; if it read none, the run says nothing at all and says it in the
	// same green.
	if judged == 0 {
		t.Error("the gate judged no table reference in any unit: either the tier stopped issuing SQL (in which case this gate is vacuous and should be retired with the runtime seam) or the reader stopped seeing it")
	}
}

// extSQLScan is one unit's result: how many table references were read, and
// what was refused. The count is what separates a clean unit from an unread one.
type extSQLScan struct {
	tables   int
	findings []string
}

// scanUnitSQL parses one unit's sources (path → source text) and judges every
// table its SQL names. Sources are passed in rather than read here so the gate's
// own test can drive it with a synthetic unit — a fixture tree that named a core
// table would have to fail this very gate to prove anything.
func scanUnitSQL(t testing.TB, unit string, sources map[string]string) extSQLScan {
	t.Helper()
	namespace, err := extension.Name(unit).Namespace()
	if err != nil {
		t.Fatalf("unit %q has no SQL namespace, so nothing can be judged against it: %v", unit, err)
	}
	prefix := namespace + "_"

	fset := token.NewFileSet()
	paths := slices.Sorted(maps.Keys(sources))
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		file, parseErr := parser.ParseFile(fset, path, sources[path], 0)
		if parseErr != nil {
			t.Fatalf("%s does not parse, and a source this gate cannot read may hold the SQL it exists to judge: %v", path, parseErr)
		}
		files = append(files, file)
	}

	consts := stringConstants(files)
	scan := extSQLScan{}
	for _, file := range files {
		for _, stmt := range sqlStrings(file, consts) {
			for _, ref := range tableRefs(stmt.text) {
				scan.tables++
				if refused := judgeTable(ref, unit, prefix); refused != "" {
					scan.findings = append(scan.findings, fmt.Sprintf("%s: %s", fset.Position(stmt.pos), refused))
				}
			}
		}
	}
	return scan
}

// judgeTable returns the refusal a reference earns, or "" when the name is the
// unit's own — schema-qualified under its namespace — or a catalog read. A CTE
// never reaches here: tableRefs resolves those against the statement that
// declared them.
func judgeTable(ref tableRef, unit, prefix string) string {
	if !ref.readable {
		return fmt.Sprintf("a statement names its table through something this gate cannot read (a call, a format verb, a runtime value): spell the table with a string constant, so the SQL the unit %s issues says which table it touches", unit)
	}
	name := strings.ToLower(strings.Trim(ref.name, `"`))
	schema, relation, qualified := strings.Cut(name, ".")
	if !qualified {
		schema, relation = "", name
	}
	switch {
	case schema == "information_schema" || schema == "pg_catalog":
		return "" // reading the catalog says nothing about another owner's rows
	case schema == "" && strings.HasPrefix(relation, "pg_"):
		return ""
	case schema == extSchema && strings.HasPrefix(relation, prefix):
		return ""
	case schema == "" && strings.HasPrefix(relation, prefix):
		return fmt.Sprintf("SQL names %q unqualified: the %s schema is on no search_path the app connects with, so this resolves to a public table the unit %s does not own — qualify it as %s.%s", name, extSchema, unit, extSchema, relation)
	}
	return fmt.Sprintf("SQL names %q: the unit %s addresses %s.%s… and nothing else, so this table is not its to read or write", name, unit, extSchema, prefix)
}

// goSources reads every .go file under dir, keyed by its slash-normalised path.
func goSources(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		src, readErr := os.ReadFile(path) // #nosec G304 -- path from walking the trusted source tree
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(path)] = string(src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// stringConstants maps every name a unit binds to a constant string — package
// consts, vars, and `:=` — so a statement spelled through them can be folded.
//
// Two passes, over the files in a fixed order, so a constant built from another
// resolves whichever file declares it first. A name bound twice is read as its
// last binding: a unit that spells one table two ways under one name is not a
// shape this tree has, and reading the later one keeps the pass deterministic.
func stringConstants(files []*ast.File) map[string]string {
	consts := map[string]string{}
	for range 2 {
		for _, file := range files {
			collectStringBindings(file, consts)
		}
	}
	return consts
}

func collectStringBindings(file *ast.File, consts map[string]string) {
	ast.Inspect(file, func(node ast.Node) bool {
		switch decl := node.(type) {
		case *ast.ValueSpec:
			for i, name := range decl.Names {
				if i < len(decl.Values) {
					if text, ok := foldString(decl.Values[i], consts); ok {
						consts[name.Name] = text
					}
				}
			}
		case *ast.AssignStmt:
			if len(decl.Lhs) != len(decl.Rhs) {
				return true
			}
			for i, target := range decl.Lhs {
				name, ok := target.(*ast.Ident)
				if !ok {
					continue
				}
				if text, folded := foldString(decl.Rhs[i], consts); folded {
					consts[name.Name] = text
				}
			}
		}
		return true
	})
}

// foldedSQL is one folded string the gate reads as a statement.
type foldedSQL struct {
	text string
	pos  token.Pos
}

// sqlStrings folds every string expression in the file and keeps the ones that
// open as a statement. A folded expression is consumed whole — the walk does not
// descend into it — so a concatenation is judged once rather than fragment by
// fragment.
func sqlStrings(file *ast.File, consts map[string]string) []foldedSQL {
	var out []foldedSQL
	ast.Inspect(file, func(node ast.Node) bool {
		expr, isExpr := node.(ast.Expr)
		if !isExpr {
			return true
		}
		text, folded := foldString(expr, consts)
		if !folded {
			return true
		}
		if looksLikeSQL(text) {
			out = append(out, foldedSQL{text: text, pos: expr.Pos()})
		}
		return false
	})
	return out
}

// foldString reads a string expression as the text it produces, standing
// computedFragment in for every part it cannot resolve. It reports false when
// the expression is not a string at all, which is what keeps the walk descending
// into a call's arguments.
func foldString(expr ast.Expr, consts map[string]string) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(node.Value)
		if err != nil {
			// A STRING literal the parser accepted and strconv cannot decode is
			// not a shape Go admits; treating it as unreadable keeps the fold
			// total without inventing text for it.
			return computedFragment, true
		}
		return text, true
	case *ast.ParenExpr:
		return foldString(node.X, consts)
	case *ast.Ident:
		if text, known := consts[node.Name]; known {
			return text, true
		}
		return computedFragment, false
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		left, leftIsString := foldString(node.X, consts)
		right, rightIsString := foldString(node.Y, consts)
		if !leftIsString && !rightIsString {
			return "", false
		}
		return left + right, true
	}
	return computedFragment, false
}

// sqlStatementShapes are the openings a folded string must have to be read as
// SQL, each with the word that separates a statement from a sentence that
// happens to start the same way. "update the note" is prose; `UPDATE … SET …`
// is not.
var sqlStatementShapes = []struct{ opener, companion string }{
	{"select", " from "},
	{"insert", " into "},
	{"update", " set "},
	{"delete", " from "},
	{"with", " as "},
	{"merge", " into "},
	{"create", " table "},
	{"alter", " table "},
	{"drop", " table "},
	{"truncate", ""},
}

func looksLikeSQL(text string) bool {
	normalised := " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	for _, shape := range sqlStatementShapes {
		if !strings.HasPrefix(normalised, " "+shape.opener+" ") {
			continue
		}
		if shape.companion == "" || strings.Contains(normalised, shape.companion) {
			return true
		}
	}
	return false
}

// tableRef is one table position in a statement: the name it holds, or the fact
// that the name could not be read.
type tableRef struct {
	name     string
	readable bool
}

var (
	// sqlNoise is what a table name cannot hide in and a keyword can falsely
	// appear in: comments, and single-quoted literals ('' escaping included).
	sqlNoise = regexp.MustCompile(`--[^\n]*|(?s)/\*.*?\*/|'(?:[^']|'')*'`)

	// sqlToken splits a statement into quoted identifiers, bare identifiers
	// (schema-qualified names held together by the dot), placeholders, and
	// single characters for everything else.
	sqlToken = regexp.MustCompile(`"[^"]*"|[A-Za-z_][A-Za-z0-9_$.]*|\$[0-9]+|\S`)
)

// tableRefs returns every table the statement names.
func tableRefs(sql string) []tableRef {
	tokens := sqlToken.FindAllString(sqlNoise.ReplaceAllString(sql, " "), -1)
	ctes := cteNames(tokens)
	var refs []tableRef
	var callStack []string
	for i, raw := range tokens {
		keyword := strings.ToLower(raw)
		switch keyword {
		case "(":
			enclosing := ""
			if i > 0 && isBareIdentifier(tokens[i-1]) {
				enclosing = strings.ToLower(tokens[i-1])
			}
			callStack = append(callStack, enclosing)
			continue
		case ")":
			if len(callStack) > 0 {
				callStack = callStack[:len(callStack)-1]
			}
			continue
		}
		if !opensTablePosition(tokens, i, callStack) {
			continue
		}
		ref, judgeable := tableAfter(tokens, i+1, keyword)
		if !judgeable || (ref.readable && ctes[strings.ToLower(ref.name)]) {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// argumentSeparators are the functions whose FROM separates arguments rather
// than naming a table: EXTRACT(epoch FROM ts), TRIM(both FROM s),
// SUBSTRING(s FROM 2), OVERLAY(s PLACING x FROM 2).
var argumentSeparators = []string{"extract", "trim", "substring", "overlay"}

// tableQualifiers stand between a keyword and the name it introduces —
// `DELETE FROM ONLY t`, `JOIN LATERAL …`, `CREATE TABLE IF NOT EXISTS t`.
var tableQualifiers = []string{"only", "lateral", "if", "not", "exists"}

// opensTablePosition reports whether the token at i is a keyword the next token
// names a table for.
func opensTablePosition(tokens []string, i int, callStack []string) bool {
	previous := ""
	if i > 0 {
		previous = strings.ToLower(tokens[i-1])
	}
	switch strings.ToLower(tokens[i]) {
	case "from":
		return len(callStack) == 0 || !slices.Contains(argumentSeparators, callStack[len(callStack)-1])
	case "join", "into", "table", "using":
		// USING carries two meanings and both are handled here: the table of a
		// `DELETE … USING t` / `MERGE … USING t`, and the column list of a
		// `JOIN … USING (a, b)`, which tableAfter drops on the opening paren.
		return true
	case "update":
		// Only a statement's own opening UPDATE names a table. Every other
		// spelling — `FOR UPDATE`, `FOR NO KEY UPDATE`, `ON CONFLICT DO UPDATE` —
		// is a clause, and the words before it are not a table's.
		return previous == "" || previous == ";" || previous == "("
	}
	return false
}

// tableAfter reads the table a keyword introduces. It reports false when the
// position holds something that is not a table at all (a subquery, a column
// list, a set-returning function) and true when there is a reference to judge —
// including one whose name it could not read.
func tableAfter(tokens []string, i int, keyword string) (tableRef, bool) {
	for i < len(tokens) && slices.Contains(tableQualifiers, strings.ToLower(tokens[i])) {
		i++
	}
	if i >= len(tokens) {
		// A statement that ends on the keyword introducing its table: the name is
		// somewhere this gate cannot see.
		return tableRef{}, true
	}
	name := tokens[i]
	if name == "(" {
		return tableRef{}, false // a subquery, or the column list of a JOIN … USING (a, b)
	}
	if !isBareIdentifier(name) && !strings.HasPrefix(name, `"`) {
		return tableRef{readable: false}, true
	}
	// A name applied to an argument list is a set-returning function, not a
	// table — but only where one can stand: `INSERT INTO t (cols)` names a table
	// and a column list, and `UPDATE t (…)` is not a spelling at all.
	if (keyword == "from" || keyword == "join") && i+1 < len(tokens) && tokens[i+1] == "(" {
		return tableRef{}, false
	}
	return tableRef{name: name, readable: true}, true
}

// cteNames collects the names a statement declares for itself: `WITH x AS (…)`,
// including the MATERIALIZED spellings. A reference to one names no table.
func cteNames(tokens []string) map[string]bool {
	names := map[string]bool{}
	for i, raw := range tokens {
		if !strings.EqualFold(raw, "as") || i == 0 || !isBareIdentifier(tokens[i-1]) {
			continue
		}
		next := i + 1
		for next < len(tokens) && (strings.EqualFold(tokens[next], "materialized") || strings.EqualFold(tokens[next], "not")) {
			next++
		}
		if next < len(tokens) && tokens[next] == "(" {
			names[strings.ToLower(tokens[i-1])] = true
		}
	}
	return names
}

func isBareIdentifier(token string) bool {
	if token == "" {
		return false
	}
	first := token[0]
	return first == '_' || (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}

// probeUnit is the synthetic unit the gate's own test is written against. Its
// namespace is ext_probe, so ext.ext_probe_note is its table and everything else
// in the cases below is somebody's else's.
const probeUnit = "probe"

// extSQLGateCase is one source the gate must judge a particular way. want is the
// text the refusal has to carry, or "" when the source must be accepted.
type extSQLGateCase struct {
	name   string
	body   string
	want   string
	tables int // table references the gate is expected to have read
}

// extSQLGateCases exercise the gate against a unit it can refuse. The real tree
// is supposed to pass, so a gate proven only by "extensions/ is currently clean"
// is one that keeps passing after it stops working — and this one has a second
// way to read green that a clean tree hides completely: seeing no SQL at all.
// Every accepted case therefore also pins how many table references were READ.
var extSQLGateCases = []extSQLGateCase{
	{
		name:   "a core table named inline",
		body:   `tx.Exec(ctx, "SELECT id FROM person WHERE id = $1")`,
		want:   `"person": the unit probe addresses ext.ext_probe_…`,
		tables: 1,
	},
	{
		name:   "a core table named through the public schema",
		body:   `tx.Exec(ctx, "DELETE FROM public.person WHERE id = $1")`,
		want:   `"public.person"`,
		tables: 1,
	},
	{
		name: "a core table reached through a constant",
		body: `tx.Exec(ctx, "SELECT id FROM "+subject+" WHERE id = $1")`,
		// The spelling the one unit shipping SQL uses for its OWN table. A gate
		// blind to the concatenation reads the whole tier green.
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "another unit's table",
		body:   `tx.Exec(ctx, "SELECT body FROM ext.ext_notes_note LIMIT 1")`,
		want:   `"ext.ext_notes_note": the unit probe addresses`,
		tables: 1,
	},
	{
		name:   "a core table joined onto the unit's own",
		body:   `tx.Exec(ctx, "SELECT n.id FROM ext.ext_probe_note n JOIN person p ON p.id = n.subject_id")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table reached around a DELETE … USING",
		body:   `tx.Exec(ctx, "DELETE FROM ext.ext_probe_note USING person WHERE person.id = ext_probe_note.subject_id")`,
		want:   `"person"`,
		tables: 2,
	},
	{
		name:   "a core table written by an INSERT",
		body:   `tx.Exec(ctx, "INSERT INTO activity (kind, subject_id) VALUES ($1, $2)")`,
		want:   `"activity"`,
		tables: 1,
	},
	{
		name:   "a core table rewritten by an UPDATE",
		body:   `tx.Exec(ctx, "UPDATE person SET full_name = $1 WHERE id = $2")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "the unit's own table left unqualified",
		body:   `tx.Exec(ctx, "SELECT id FROM ext_probe_note LIMIT 1")`,
		want:   "the ext schema is on no search_path",
		tables: 1,
	},
	{
		name:   "a scratch table created over a core name",
		body:   `tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS person (id uuid)")`,
		want:   `"person"`,
		tables: 1,
	},
	{
		name:   "a table name assembled at runtime",
		body:   `tx.Exec(ctx, fmt.Sprintf("SELECT id FROM %s LIMIT 1", subject))`,
		want:   "this gate cannot read",
		tables: 1,
	},
	{
		name:   "the unit's own table, schema-qualified through a constant",
		body:   `tx.Exec(ctx, "SELECT id FROM "+noteTable+" ORDER BY id LIMIT 1")`,
		tables: 1,
	},
	{
		name:   "a CTE the same statement declares",
		body:   `tx.Exec(ctx, "WITH stale AS (SELECT id FROM "+noteTable+" ORDER BY id DESC OFFSET 50) DELETE FROM "+noteTable+" WHERE id IN (SELECT id FROM stale)")`,
		tables: 2,
	},
	{
		name:   "a catalog read",
		body:   `tx.Exec(ctx, "SELECT column_name FROM information_schema.columns WHERE table_name = $1")`,
		tables: 1,
	},
	{
		name:   "EXTRACT's argument separator",
		body:   `tx.Exec(ctx, "SELECT extract(epoch FROM created_at) FROM "+noteTable)`,
		tables: 1,
	},
	{
		name:   "a row lock and an upsert clause",
		body:   `tx.Exec(ctx, "INSERT INTO "+noteTable+" (id) VALUES ($1) ON CONFLICT (id) DO UPDATE SET body = $2 RETURNING (SELECT body FROM "+noteTable+" WHERE id = $1 FOR UPDATE)")`,
		tables: 2,
	},
	{
		name:   "a set-returning function and a USING column list",
		body:   `tx.Exec(ctx, "SELECT n.id FROM "+noteTable+" n JOIN unnest($1::uuid[]) AS wanted(id) USING (id)")`,
		tables: 1,
	},
	{
		name:   "prose that merely reads like SQL",
		body:   `_ = "hello from the demo extension"; _ = "update the note, then select the row"`,
		tables: 0,
	},
}

// TestExtensionSQLScopeRefusesWhatItMust drives the gate with sources the real
// tree does not contain.
func TestExtensionSQLScopeRefusesWhatItMust(t *testing.T) {
	for _, probe := range extSQLGateCases {
		t.Run(probe.name, func(t *testing.T) {
			scan := scanUnitSQL(t, probeUnit, map[string]string{"probe.go": probeSource(probe.body)})
			if probe.tables != scan.tables {
				t.Errorf("the gate read %d table reference(s), want %d — a case whose SQL the reader never saw proves nothing about the verdict below", scan.tables, probe.tables)
			}
			switch {
			case probe.want == "" && len(scan.findings) > 0:
				t.Errorf("the gate refused what it must accept: %s", strings.Join(scan.findings, "; "))
			case probe.want == "":
			case len(scan.findings) != 1:
				t.Errorf("the gate returned %d finding(s), want the one refusing %q: %s", len(scan.findings), probe.want, strings.Join(scan.findings, "; "))
			case !strings.Contains(scan.findings[0], probe.want):
				t.Errorf("the gate refused the source but the reason does not mention %q: %s", probe.want, scan.findings[0])
			}
		})
	}
}

// probeSource wraps a case body in a compilable unit whose constants are the
// two a real unit declares: its own table, and — for the cases that reach past
// it — a core one.
func probeSource(body string) string {
	return `package probe

const noteTable = "ext.ext_probe_note"
const subject = "person"

func run(ctx context.Context, tx Tx) { ` + body + ` }
`
}
