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
// The fold has no scopes, and the rules that make that safe are the two the
// gate would otherwise be wrong under. A name resolves only when every binding
// of it in the unit folds to the SAME text, so one function's `table :=
// "person"` is never answered by another function's `table := "ext.…"`; and a
// name bound even once to something unreadable resolves to nothing, so a value
// the fold could read does not stand in for one it could not. Under both rules
// the statement reads as computed — a finding — rather than as allowed.
//
// The allowlist is the unit's namespace, not a list of core tables: a table is
// the unit's own when it is `ext.<namespace>_…` — derived through the same
// extension.Name(…).Namespace() the migration gate and the unit's database role
// come from, so the three cannot drift. A bare, unqualified name is refused too,
// and for the reason notes' own constant gives: the ext schema is on no
// search_path the app connects with, so `ext_notes_note` unqualified is a public
// table the unit does not own.
//
// WHAT IT CANNOT SEE, so that nobody reads more into a green run than it says: a
// statement built by anything other than `+` over constants (a Sprintf whose
// verb is the table, a strings.Builder, a query assembled in a loop) is read as
// far as its literals go and no further. That is the same bound as the header's
// first paragraph and the reason the wall is #628's, not this file's.

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
	// Sorted, because two units each holding a finding must report in the same
	// order twice: a merge gate whose output shuffles between runs is one a
	// reader cannot diff.
	for _, dir := range slices.Sorted(maps.Keys(trees)) {
		scan := scanUnitSQL(t, trees[dir], goSources(t, dir))
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
	files := make([]*ast.File, 0, len(sources))
	for _, path := range slices.Sorted(maps.Keys(sources)) {
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
		return fmt.Sprintf("a statement names its table through something this gate cannot read (a call, a format verb, a name bound more than one way): spell the table with a string constant, so the SQL the unit %s issues says which table it touches", unit)
	}
	schema, relation := splitQualified(ref.name)
	switch {
	case schema == "information_schema" || schema == "pg_catalog":
		return "" // reading the catalog says nothing about another owner's rows
	case schema == "" && strings.HasPrefix(relation, "pg_"):
		return ""
	case schema == extSchema && strings.HasPrefix(relation, prefix):
		return ""
	case schema == "" && strings.HasPrefix(relation, prefix):
		return fmt.Sprintf("SQL names %q unqualified: the %s schema is on no search_path the app connects with, so this resolves to a public table the unit %s does not own — qualify it as %s.%s", ref.name, extSchema, unit, extSchema, relation)
	}
	return fmt.Sprintf("SQL names %q: the unit %s addresses %s.%s… and nothing else, so this table is not its to read or write", ref.name, unit, extSchema, prefix)
}

// splitQualified reads a possibly-qualified name as (schema, relation), taking
// the LAST two parts so that a database-qualified `db.public.person` is judged
// on `public.person`. Quoting is stripped per part, because PostgreSQL quotes
// each identifier separately — `"ext"."ext_notes_note"` is one reference — and
// the result is lower-cased, which is what an unquoted identifier folds to.
func splitQualified(name string) (schema, relation string) {
	parts := identParts(name)
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

// identParts splits a qualified name on the dots OUTSIDE quotes, so a quoted
// identifier holding one ("a.b") stays whole.
func identParts(name string) []string {
	var parts []string
	current := strings.Builder{}
	quoted := false
	for _, r := range name {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '.' && !quoted:
			parts = append(parts, strings.ToLower(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	return append(parts, strings.ToLower(current.String()))
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

// stringBinding is one place a unit binds a name to a value — a const, a var,
// or an assignment.
type stringBinding struct {
	name string
	expr ast.Expr
}

// maxConstantPasses bounds the fold's fixed point. Each pass resolves one more
// link of a constant built from constants, so the bound is the longest chain
// this reader follows; past it the map is used as it stands, which can only
// leave a name unresolved (and its SQL unread), never wrongly resolved.
const maxConstantPasses = 8

// stringConstants maps every name a unit binds to a constant string — package
// consts, vars, and `:=` — so a statement spelled through them can be folded.
//
// A name resolves only when EVERY binding of it in the unit folds to the same
// text. Bind it twice to different strings, or once to something the fold
// cannot read (a function call, another package's value), and the name resolves
// to nothing at all: the SQL spelled through it then reads as computed, which is
// a finding rather than a silent pass. That rule is what keeps one function's
// `table := "person"` from being answered by another function's `table :=
// "ext.ext_notes_note"` — the fold is name-keyed across the whole unit and has
// no scopes of its own.
func stringConstants(files []*ast.File) map[string]string {
	all := make([]stringBinding, 0, 16)
	for _, file := range files {
		all = append(all, stringBindings(file)...)
	}
	consts := map[string]string{}
	for range maxConstantPasses {
		next := map[string]string{}
		unreadable := map[string]bool{}
		for _, binding := range all {
			text, folded := foldString(binding.expr, consts)
			prior, bound := next[binding.name]
			switch {
			case !folded, bound && prior != text:
				unreadable[binding.name] = true
			default:
				next[binding.name] = text
			}
		}
		for name := range unreadable {
			delete(next, name)
		}
		if maps.Equal(next, consts) {
			break
		}
		consts = next
	}
	return consts
}

// stringBindings walks one file and reports every name it binds, in source
// order. The blank identifier is skipped: it binds nothing a statement can name.
func stringBindings(file *ast.File) []stringBinding {
	var out []stringBinding
	ast.Inspect(file, func(node ast.Node) bool {
		switch decl := node.(type) {
		case *ast.ValueSpec:
			for i, name := range decl.Names {
				if i < len(decl.Values) && name.Name != "_" {
					out = append(out, stringBinding{name: name.Name, expr: decl.Values[i]})
				}
			}
		case *ast.AssignStmt:
			if len(decl.Lhs) != len(decl.Rhs) {
				return true
			}
			for i, target := range decl.Lhs {
				if name, ok := target.(*ast.Ident); ok && name.Name != "_" {
					out = append(out, stringBinding{name: name.Name, expr: decl.Rhs[i]})
				}
			}
		}
		return true
	})
	return out
}

// foldedSQL is one folded string the gate reads as a statement, already
// stripped of the comments and literals a keyword can hide in.
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
		switch expr.(type) {
		case *ast.BasicLit, *ast.BinaryExpr, *ast.ParenExpr:
		default:
			// A bare name is not a statement even when it resolves to one. The
			// fold answers for a name so a concatenation can be read through it;
			// judging the name on its own would report the statement again at
			// its declaration, at every call that passes it, and at every
			// comparison against it — three reports of one mistake, none of them
			// where the SQL is written.
			return true
		}
		text, folded := foldString(expr, consts)
		if !folded {
			return true
		}
		// Stripped BEFORE the shape test, not after: a statement that opens with
		// its own `-- why` comment is still a statement, and testing the raw text
		// would read that comment as the first word and skip the query.
		cleaned := sqlNoise.ReplaceAllString(text, " ")
		if looksLikeSQL(cleaned) {
			out = append(out, foldedSQL{text: cleaned, pos: expr.Pos()})
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
	// sqlNoise is what a keyword can falsely appear in and a table name cannot
	// hide in: comments, single-quoted literals ('' escaping included), and
	// dollar-quoted bodies. The dollar-quote pattern cannot pair a tag with its
	// own closing tag (RE2 has no backreference), so it matches the nearest
	// closing delimiter of the same SHAPE — which over-strips only a statement
	// carrying two differently-tagged bodies, and under-strips nothing.
	sqlNoise = regexp.MustCompile(`--[^\n]*|(?s)/\*.*?\*/|'(?:[^']|'')*'|(?s)\$[A-Za-z0-9_]*\$.*?\$[A-Za-z0-9_]*\$`)

	// sqlToken splits a statement into names (a dotted chain of bare or quoted
	// identifiers, held together so `"ext"."ext_notes_note"` is one token),
	// placeholders, and single characters for everything else.
	sqlToken = regexp.MustCompile(`(?:"[^"]*"|[A-Za-z_][A-Za-z0-9_$]*)(?:\.(?:"[^"]*"|[A-Za-z_][A-Za-z0-9_$]*))*|\$[0-9]+|\S`)
)

// tableRefs returns every table the statement names.
func tableRefs(sql string) []tableRef {
	tokens := sqlToken.FindAllString(sql, -1)
	ctes := cteScopes(tokens)
	var refs []tableRef
	var callStack []string
	judged := map[int]bool{}
	for i, raw := range tokens {
		switch raw {
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
		for _, at := range tableTargets(tokens, i) {
			// One index is judged once. `TRUNCATE TABLE t` reaches t twice —
			// once past the qualifier TRUNCATE steps over, once from TABLE
			// itself — and the same table counted twice would read as two
			// findings for one mistake.
			if judged[at] {
				continue
			}
			judged[at] = true
			ref := refAt(tokens, at)
			if ref.readable && withinCTEScope(ctes, ref.name, at) {
				continue
			}
			refs = append(refs, ref)
		}
	}
	return refs
}

// argumentSeparators are the functions whose FROM separates arguments rather
// than naming a table: EXTRACT(epoch FROM ts), TRIM(both FROM s),
// SUBSTRING(s FROM 2), OVERLAY(s PLACING x FROM 2).
var argumentSeparators = []string{"extract", "trim", "substring", "overlay"}

// clauseWords end a table list. They are what tells `FROM a, b WHERE c` from
// `FROM a, b` — an alias is any other word, so the list has to know where it
// stops.
var clauseWords = []string{
	"where", "group", "having", "window", "order", "limit", "offset", "fetch",
	"for", "union", "intersect", "except", "returning", "set", "values", "on",
	"using", "join", "left", "right", "inner", "outer", "full", "cross",
	"natural", "lateral", "select", "insert", "update", "delete", "with", "into",
}

// tableQualifiers stand between a keyword and the name it introduces —
// `DELETE FROM ONLY t`, `JOIN LATERAL …`, `TRUNCATE TABLE t`,
// `CREATE TABLE IF NOT EXISTS t`.
var tableQualifiers = []string{"only", "lateral", "table", "if", "not", "exists"}

// nonTableUpdatePrefixes are the words a clause's UPDATE follows: `FOR UPDATE`,
// `FOR NO KEY UPDATE`, `ON CONFLICT … DO UPDATE`. Every other UPDATE names the
// table it is about to rewrite — including the one in `WITH x AS (…) UPDATE t`,
// which is why this is a denylist and not a list of positions UPDATE may open in.
var nonTableUpdatePrefixes = []string{"for", "do", "key"}

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
	case "join", "into", "table", "truncate", "using":
		// USING carries two meanings and both are handled here: the table of a
		// `DELETE … USING t` / `MERGE … USING t`, and the column list of a
		// `JOIN … USING (a, b)`, which tableTargets drops on the opening paren.
		return true
	case "update":
		return !slices.Contains(nonTableUpdatePrefixes, previous)
	}
	return false
}

// tableTargets returns the token indices the keyword at i introduces as table
// names: one for most keywords, and after FROM every entry of a comma-separated
// list, because `FROM ext.ext_notes_note n, person p` is a join with no JOIN in
// it. An index at or past the end means the statement stopped on the keyword and
// the name is somewhere this gate cannot see.
func tableTargets(tokens []string, i int) []int {
	keyword := strings.ToLower(tokens[i])
	fromList := keyword == "from"
	var targets []int
	at := i + 1
	for {
		for at < len(tokens) && slices.Contains(tableQualifiers, strings.ToLower(tokens[at])) {
			at++
		}
		if at >= len(tokens) {
			return append(targets, at)
		}
		if !namesATable(tokens, at, keyword) {
			return targets
		}
		targets = append(targets, at)
		if !fromList {
			return targets
		}
		next := skipAlias(tokens, at+1)
		if next >= len(tokens) || tokens[next] != "," {
			return targets
		}
		at = next + 1
	}
}

// namesATable reports whether the token at i can be a table name at all. A `(`
// opens a subquery or a column list. A name applied to an argument list is a
// set-returning function — but only where one can stand: `INSERT INTO t (cols)`
// names a table and its columns, and `CREATE TABLE t (…)` a table and its
// definition. A token that is neither a paren nor a name IS judged, as a
// reference whose name could not be read.
func namesATable(tokens []string, i int, keyword string) bool {
	if tokens[i] == "(" {
		return false
	}
	if keyword != "from" && keyword != "join" {
		return true
	}
	return !isName(tokens[i]) || i+1 >= len(tokens) || tokens[i+1] != "("
}

// refAt classifies the token at i as the table reference it holds.
func refAt(tokens []string, i int) tableRef {
	if i >= len(tokens) || !isName(tokens[i]) {
		return tableRef{}
	}
	return tableRef{name: tokens[i], readable: true}
}

// skipAlias steps over the alias a table may carry — `t x`, `t AS x`, and the
// column list either spelling may take — and returns where the entry ends. A
// clause word is never an alias: it is what the list stops at.
func skipAlias(tokens []string, i int) int {
	if i < len(tokens) && strings.EqualFold(tokens[i], "as") {
		i++
	}
	if i < len(tokens) && isName(tokens[i]) && !slices.Contains(clauseWords, strings.ToLower(tokens[i])) {
		i++
	}
	if i < len(tokens) && tokens[i] == "(" {
		i = matchingParen(tokens, i) + 1
	}
	return i
}

// cteScope is the token span of one WITH-declared name's body.
type cteScope struct {
	start, end int
	recursive  bool
}

// cteScopes collects the names a statement declares for itself: `WITH x AS (…)`,
// including the MATERIALIZED spellings and a column list (`WITH x(id) AS (…)`).
//
// The BODY is recorded, not just the name, because a CTE shadows a real table
// only where it is in scope: in `WITH person AS (SELECT id FROM person)` the
// inner name still reads the core table, and exempting it everywhere would hand
// a unit a one-line way past this gate. A RECURSIVE WITH is the exception —
// there the body legitimately names the CTE itself.
func cteScopes(tokens []string) map[string]cteScope {
	recursive := false
	for i, raw := range tokens {
		if strings.EqualFold(raw, "with") && i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "recursive") {
			recursive = true
		}
	}
	scopes := map[string]cteScope{}
	for i, raw := range tokens {
		if !strings.EqualFold(raw, "as") || i == 0 {
			continue
		}
		name := i - 1
		if tokens[name] == ")" {
			name = matchingOpen(tokens, name) - 1 // a column list: WITH x(id) AS (…)
		}
		if name < 0 || !isBareIdentifier(tokens[name]) {
			continue
		}
		body := i + 1
		for body < len(tokens) && (strings.EqualFold(tokens[body], "materialized") || strings.EqualFold(tokens[body], "not")) {
			body++
		}
		if body < len(tokens) && tokens[body] == "(" {
			scopes[strings.ToLower(tokens[name])] = cteScope{start: body, end: matchingParen(tokens, body), recursive: recursive}
		}
	}
	return scopes
}

// withinCTEScope reports whether the reference at index at reads a CTE rather
// than a table: anywhere for a recursive WITH, and after the body otherwise.
func withinCTEScope(scopes map[string]cteScope, name string, at int) bool {
	scope, declared := scopes[strings.ToLower(strings.Trim(name, `"`))]
	return declared && (scope.recursive || at > scope.end)
}

// matchingParen returns the index of the ")" closing the "(" at open, or the
// last token when the statement never closes it.
func matchingParen(tokens []string, open int) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch tokens[i] {
		case "(":
			depth++
		case ")":
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(tokens) - 1
}

// matchingOpen is matchingParen read backwards, from a ")" to its "(".
func matchingOpen(tokens []string, closing int) int {
	depth := 0
	for i := closing; i >= 0; i-- {
		switch tokens[i] {
		case ")":
			depth++
		case "(":
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return 0
}

// isName reports whether the token can be an identifier — bare, quoted, or a
// dotted chain of either.
func isName(token string) bool {
	return isBareIdentifier(token) || strings.HasPrefix(token, `"`)
}

func isBareIdentifier(token string) bool {
	if token == "" {
		return false
	}
	first := token[0]
	return first == '_' || (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}
