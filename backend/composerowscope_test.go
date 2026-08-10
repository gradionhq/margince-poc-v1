// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Review-loop rule 3 as a fitness function over the compose tier: anything
// that returns a record is a read, so a query that hands back a REFERENCE to a
// row-scoped record applies that record's row scope.
//
// The class this closes is the reference held across time. A read model, a link
// row and a graph edge all store somebody else's record id, and the scope was
// checked when the id was WRITTEN — by which point the deal can be reassigned,
// the person merged, the org's owner moved teams. The read that hands the id
// back inherits nothing from that write, and the failure is quiet: the caller
// gets a well-formed answer naming a record whose own read path refuses them.
//
// Why the compose tier is the frame. A module never imports a sibling, so a
// cross-module read model assembled here has no sibling store's gate in front
// of it — compose is where these references are joined and served.
// rbacgate_test.go holds internal/modules' store entry points and says in its
// own header that it pins the OBJECT half, "row-scope composition itself stays
// a call-site obligation"; its entryPointsOutsideModules set then ratifies the
// compose read services as subjects it has deliberately not judged. This gate
// is that judgement, for the half rbacgate leaves open. Extending it over
// internal/modules is a separate change with its own evidence — that tier holds
// several times as many statements of this shape, and a store that owns its
// table is a different question from a read model pointing at someone else's.
//
// What the gate asks, in three derived steps:
//
//   - the row-scoped VOCABULARY is read out of platform/auth's own
//     ownerScopedTables, so a table that becomes row-scoped widens this census
//     without anyone remembering to;
//   - the SITES are every SQL select list in the tier naming a `<table>_id`
//     column for one of those tables, resolved out of the string literals
//     themselves — a new read model inherits the obligation — minus the reads
//     that only re-key ids the caller passed IN (boundToCallerArgument);
//   - the OBLIGATION is that the enclosing function transitively reaches a
//     row-scope spelling. Object admission (auth.Require) deliberately does not
//     count: both halves of #632 passed auth.Require(ctx, "deal", ActionRead)
//     and served a deal the caller could not open, so a gate that accepted it
//     would have read green over the defect it was written for.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

const (
	// composeTier is the subtree this gate judges.
	composeTier = "internal/compose"

	// rowScopeVocabularyPkg holds ownerScopedTables — the closed set of tables
	// the row-scope primitives will interpolate, and therefore the closed set of
	// references this gate is about.
	rowScopeVocabularyPkg = "internal/platform/auth"

	// wantMinimumScopedSites guards against the way this gate fails silently: an
	// extractor that stops recognising SQL finds no sites and reports nothing,
	// which is indistinguishable from a clean tier. Fifteen stand today; the
	// floor sits below that rather than on it, so removing a read stays an
	// ordinary change and only a collapse is a finding.
	wantMinimumScopedSites = 10
)

// unscopedReferenceReads ratifies a compose read that hands back a row-scoped
// record's id without applying that record's row scope. Keyed
// "package-dir:FuncName", each entry stating what stands in for the clause.
var unscopedReferenceReads = gatekit.Waive(map[string]string{
	// Signal producer. It runs inside signalScanWorkspaceWorker, which binds
	// PrincipalSystem "agent:signal-scan" before the read (jobs_signals.go),
	// so there is no human actor for a row scope to narrow to and the org id
	// goes to the signal being written rather than to any caller. What a REP may
	// then see of those signals is decided on the read side, by
	// auth.SignalScopeClause.
	"internal/compose:scanGhostedThreads": "the ghosted-thread rule's account scan, under the signal-scan sweep's system principal: the organization it names is what the signal is ABOUT, and it is handed to signals.RecordDerived, never to a reader",

	"internal/compose:employerOf": "the person auto-enrich consumer's employer resolution, under the PrincipalSystem actor its own systemContext binds before the pass (compose/personautoenrich.go): it answers which company's published site may describe this person, and the id is spent inside the same transaction choosing that site — a caller never sees it",

	"internal/compose/briefs:briefEvidenceRows": "the ranking's stakeholder gather, whose person ids never leave Rank: they are consumed only by resolveWarmth, which routes each one through the injected people-strength seam and floors the warmth factor on ErrNotFound/ErrPermissionDenied, so a stakeholder outside the caller's scope contributes nothing and is never named. The evidence the run PERSISTS is the deal and its activity rows (TestBriefEvidenceIsTheDealThenItsActivityRows holds that), not these",

	"internal/compose/network:readDealFacts": "the coverage view's deal row: the organization id it reads is spent one function later on readDeparted's employment test and is absent from DealCoverage, so it reaches no caller. The DEAL is gated where the reference enters — network.Reads.GetDealCoverage takes auth.Require plus auth.EnsureVisibleLive on it before opening this assembly",
})

// rowScopeSpellings are the platform/auth entry points that APPLY a row scope.
//
// auth.Require and its siblings are absent on purpose: they answer whether the
// principal may touch this KIND of record, which is a different question and
// the one both #632 sites already passed. auth.Unbounded/UnboundedFor are
// absent for the mirror reason — they are how a caller asks whether it may SKIP
// the clause, so on their own they apply nothing.
var rowScopeSpellings = map[string]bool{
	"ScopeClause": true, "ScopeClauseFor": true,
	"OwnerPredicate": true, "VisiblePredicate": true,
	"EnsureVisible": true, "EnsureVisibleLive": true, "EnsureVisibleForSubjectRights": true,
	"EnsureLinkTarget": true, "VisibleTo": true, "LinkTargetVisibleClause": true,
	"ActivityScopeClause": true, "EnsureActivityVisible": true, "EnsureActivityVisibleLive": true,
	"SignalScopeClause": true, "EnsureSignalVisible": true, "EnsureSignalVisibleLive": true,
	"RelationshipEndpointScope": true, "EnsureRelationshipVisible": true,
}

// referenceSite is one SQL select list in the compose tier that names a
// row-scoped record's id column.
type referenceSite struct {
	dir, recv, fn string
	line          int
	table         string
}

func TestEveryComposeReadOfARecordReferenceAppliesItsRowScope(t *testing.T) {
	defer unscopedReferenceReads.AssertAllMatched(t)

	tables := rowScopedTables(t)
	pkgs, sites := referenceSites(t, tables)
	if len(sites) < wantMinimumScopedSites {
		t.Fatalf("only %d record-reference reads found in %s, want at least %d — the SQL extractor lost its source",
			len(sites), composeTier, wantMinimumScopedSites)
	}

	for _, site := range sites {
		if reachesRowScope(pkgs[site.dir].visibleTo(site.recv), site.fn, map[string]bool{}) {
			continue
		}
		if unscopedReferenceReads.Waived(t, site.dir+":"+site.fn) {
			continue
		}
		t.Errorf("%s:%d: %s hands back a %s reference and reaches no row-scope clause (directly or via same-package "+
			"helpers) — a persisted reference is re-checked when it is SERVED, not trusted from when it was written; "+
			"apply auth.ScopeClauseFor/EnsureVisible for %q, or ratify the read in unscopedReferenceReads",
			site.dir, site.line, site.fn, site.table, site.table)
	}
}

// rowScopedTables reads platform/auth's own ownerScopedTables, resolving the
// table-name consts the map is keyed by. Deriving the vocabulary rather than
// restating it is what makes a newly row-scoped table widen this census on its
// own; a restated copy would go quietly stale instead.
func rowScopedTables(t *testing.T) map[string]bool {
	t.Helper()
	consts := map[string]string{}
	// Keyed by the key as WRITTEN, valued by whether it was written as an
	// identifier — a const still owed a resolution — rather than as a literal
	// that already IS the table name.
	var tables map[string]bool
	for _, src := range tierFiles(t, rowScopeVocabularyPkg) {
		for _, decl := range src.File.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				if gen.Tok == token.CONST {
					if text, ok := stringConst(value.Values[0]); ok {
						consts[value.Names[0].Name] = text
					}
					continue
				}
				if gen.Tok == token.VAR && value.Names[0].Name == "ownerScopedTables" {
					tables = map[string]bool{}
					collectMapKeys(t, value.Values[0], tables)
				}
			}
		}
	}
	if len(tables) == 0 {
		t.Fatalf("%s declares no ownerScopedTables map — teach this gate where the row-scope vocabulary moved", rowScopeVocabularyPkg)
	}
	// The const pass and the map pass run over the same file set, so a key
	// spelled as an identifier resolves only after both are collected.
	resolved := make(map[string]bool, len(tables))
	for key, spelledAsIdent := range tables {
		text, isConst := consts[key]
		switch {
		case isConst:
			resolved[text] = true
		case spelledAsIdent:
			// An identifier this pass did not collect a const for resolves to
			// nothing, and taking its NAME for a table name would drop a real
			// table out of the vocabulary while the census went on reading
			// green over the reads that reference it. That is the quiet
			// narrowing this gate exists to refuse, so it refuses it here too
			// rather than only in the tier it judges.
			t.Fatalf("%s: ownerScopedTables is keyed by %s, which resolves to no string const this pass collected "+
				"(it reads single-name const specs in this package only) — teach this gate where the table name is declared",
				rowScopeVocabularyPkg, key)
		default:
			resolved[key] = true
		}
	}
	return resolved
}

// collectMapKeys records a map literal's keys as written — a string literal by
// its text, an identifier by its name — saying which of the two each was, so
// an identifier that never resolves is a finding rather than a table name.
func collectMapKeys(t *testing.T, expr ast.Expr, into map[string]bool) {
	t.Helper()
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("%s: ownerScopedTables is no longer a composite literal — teach this gate the new shape", rowScopeVocabularyPkg)
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: ownerScopedTables holds a non key/value element — teach this gate the new shape", rowScopeVocabularyPkg)
		}
		switch key := kv.Key.(type) {
		case *ast.Ident:
			into[key.Name] = true
		case *ast.BasicLit:
			text, ok := stringConst(key)
			if !ok {
				t.Fatalf("%s: ownerScopedTables is keyed by a non-string literal", rowScopeVocabularyPkg)
			}
			into[text] = false
		default:
			t.Fatalf("%s: ownerScopedTables holds an unreadable key %T", rowScopeVocabularyPkg, kv.Key)
		}
	}
}

func stringConst(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	return text, err == nil
}

// rowScopeFnInfo is what this gate needs about one function: whether its body
// applies a row scope, and the names it mentions (the resolution edges).
type rowScopeFnInfo struct {
	scoped bool
	calls  map[string]bool
}

// rowScopePkg is one package's function index, bucketed by receiver — the ""
// bucket holding package-level functions, which every receiver may call. The
// bucketing is rbacgate's, and for its reason: a handler and a store in one
// package routinely spell the same method name, and a flat index lets one
// answer for the other.
type rowScopePkg map[string]map[string]*rowScopeFnInfo

func (p rowScopePkg) visibleTo(recv string) map[string]*rowScopeFnInfo {
	fns := make(map[string]*rowScopeFnInfo, len(p[""])+len(p[recv]))
	for name, info := range p[""] {
		fns[name] = info
	}
	for name, info := range p[recv] {
		if pkgLevel, both := fns[name]; both {
			// Two same-named functions the index cannot tell apart at a call
			// site: union them into a third value rather than folding one into
			// the other, which would leak this receiver's edges into the next.
			merged := &rowScopeFnInfo{scoped: pkgLevel.scoped || info.scoped, calls: map[string]bool{}}
			for _, src := range []*rowScopeFnInfo{pkgLevel, info} {
				for call := range src.calls {
					merged.calls[call] = true
				}
			}
			fns[name] = merged
			continue
		}
		fns[name] = info
	}
	return fns
}

// reachesRowScope resolves the obligation transitively over same-package calls;
// seen breaks recursion cycles.
func reachesRowScope(fns map[string]*rowScopeFnInfo, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	info, indexed := fns[name]
	if !indexed {
		return false
	}
	if info.scoped {
		return true
	}
	for call := range info.calls {
		if _, indexed := fns[call]; indexed && reachesRowScope(fns, call, seen) {
			return true
		}
	}
	return false
}

// referenceSites indexes the compose tier and returns every SQL select list in
// it that names a row-scoped record's id column, with the function it sits in.
func referenceSites(t *testing.T, tables map[string]bool) (map[string]rowScopePkg, []referenceSite) {
	t.Helper()
	pkgs := map[string]rowScopePkg{}
	var sites []referenceSite
	for _, src := range tierFiles(t, composeTier) {
		dir := filepath.ToSlash(filepath.Dir(src.Path))
		if pkgs[dir] == nil {
			pkgs[dir] = rowScopePkg{}
		}
		for _, decl := range src.File.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv := receiverName(fn)
			if pkgs[dir][recv] == nil {
				pkgs[dir][recv] = map[string]*rowScopeFnInfo{}
			}
			info := pkgs[dir][recv][fn.Name.Name]
			if info == nil {
				info = &rowScopeFnInfo{calls: map[string]bool{}}
				pkgs[dir][recv][fn.Name.Name] = info
			}
			// Seeded without a line: every site reports the line of the SQL that
			// holds the reference, which is what a reader has to go and look at.
			at := referenceSite{dir: dir, recv: recv, fn: fn.Name.Name}
			sites = append(sites, indexFuncBody(fn, info, tables, at, src)...)
		}
	}
	return pkgs, sites
}

// indexFuncBody records one function's row-scope calls and edges, and returns
// the reference sites its SQL holds.
func indexFuncBody(fn *ast.FuncDecl, info *rowScopeFnInfo, tables map[string]bool, at referenceSite, src tierFile) []referenceSite {
	var sites []referenceSite
	// A statement assembled by `+` is read as ONE query, and its parts are not
	// read again on their own. Half a statement is the shape that reads green
	// for the wrong reason: the projection sits in the first fragment and the
	// predicate that bounds it in the last, so each half alone looks unbounded.
	joined := map[ast.Node]bool{}
	record := func(sql string, pos token.Pos) {
		for _, ref := range referencedTables(sql, tables) {
			site := at
			site.table, site.line = ref.table, src.Line(pos)+ref.lineOffset
			sites = append(sites, site)
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// CALLS, not every selector: `item.Rank` is a struct field and
			// `BriefEngine.Rank` is the ranking pass, and a name-keyed index
			// that reads the first as an edge to the second lets a scoped
			// function vouch for an unscoped one that merely shares a word.
			// That is not hypothetical — it is what let a first draft of this
			// gate pass over both halves of #632.
			switch fun := node.Fun.(type) {
			case *ast.Ident:
				info.calls[fun.Name] = true
			case *ast.SelectorExpr:
				if pkg, isPkg := fun.X.(*ast.Ident); isPkg && pkg.Name == "auth" {
					if rowScopeSpellings[fun.Sel.Name] {
						info.scoped = true
					}
					return true
				}
				info.calls[fun.Sel.Name] = true
			}
		case *ast.BinaryExpr:
			if node.Op != token.ADD || joined[node] {
				return true
			}
			if sql, holdsLiteral := concatenatedSQL(node, joined); holdsLiteral {
				record(sql, node.Pos())
			}
		case *ast.BasicLit:
			if text, isString := stringConst(node); isString && !joined[node] {
				record(text, node.Pos())
			}
		}
		return true
	})
	return sites
}

// concatenatedSQL flattens a `+` chain into the one string it builds, marking
// the literals it consumed so the walk does not re-read them alone. A part that
// is not a literal — an interpolated clause, a helper's return — renders as an
// inert placeholder: it stands for text this gate cannot see, and must not read
// as a predicate the query does not provably have.
func concatenatedSQL(expr ast.Expr, joined map[ast.Node]bool) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		text, isString := stringConst(node)
		if !isString {
			return " ~ ", false
		}
		joined[node] = true
		return text, true
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return " ~ ", false
		}
		joined[node] = true
		left, leftLiteral := concatenatedSQL(node.X, joined)
		right, rightLiteral := concatenatedSQL(node.Y, joined)
		return left + right, leftLiteral || rightLiteral
	case *ast.ParenExpr:
		return concatenatedSQL(node.X, joined)
	default:
		return " ~ ", false
	}
}

// tableReference is one row-scoped record id found in a select list, with how
// many lines into the SQL literal it sits.
type tableReference struct {
	table      string
	lineOffset int
}

// fkColumn matches a qualified or bare `<something>_id` column reference.
var fkColumn = regexp.MustCompile(`\b(?:[A-Za-z_][\w]*\.)?([a-z_]+)_id\b`)

// referencedTables finds the row-scoped record ids a SQL statement's SELECT
// LISTS hand back. The list, not the whole statement: a `<table>_id` in a WHERE
// or a JOIN condition is how a query NARROWS, and reading those as disclosures
// would flag the row-scope clauses themselves.
func referencedTables(sql string, tables map[string]bool) []tableReference {
	projected := projectedBytes(sql)
	var found []tableReference
	seen := map[string]bool{}
	for _, at := range fkColumn.FindAllStringSubmatchIndex(sql, -1) {
		table := sql[at[2]:at[3]]
		if !projected[at[0]] || !tables[table] || seen[table] || boundToCallerArgument(sql, table) {
			continue
		}
		seen[table] = true
		found = append(found, tableReference{table: table, lineOffset: strings.Count(sql[:at[0]], "\n")})
	}
	return found
}

// projectedBytes marks which bytes of a statement sit in a SELECT's projection.
//
// It is a two-pass mask because the two are not the same set. A subquery inside
// a projection is projected — a scalar subselect hands its column back through
// the outer row — but only up to its OWN from: everything after that is the
// subquery's own reading, and a correlated `WHERE e.person_id = p.id` there is
// a join condition wearing the outer projection's clothes. So: unmask every
// projection, then re-mask every select's from-onwards region.
func projectedBytes(sql string) []bool {
	selects := selectLists(sql)
	projected := make([]bool, len(sql)+1)
	for _, s := range selects {
		for i := s.from; i < s.to; i++ {
			projected[i] = true
		}
	}
	for _, s := range selects {
		for i := s.to; i < s.stop; i++ {
			projected[i] = false
		}
	}
	return projected
}

// boundToCallerArgument reports whether the statement also FILTERS on the same
// id column against a query argument — `person_id = ANY($1)`, `deal_id = $2`.
//
// Such a read answers a subset of the ids it was handed, so it discloses no
// reference the caller did not already hold; the row scope belongs on the read
// that produced that list, one level up, and probing again here would be a
// second enforcement of one rule with its own way of being wrong. org360's
// contact sections and network's departure test are both written that way and
// say so in their own comments.
//
// The limit is worth stating rather than discovering: the gate then trusts the
// CALLER to have scoped the ids it passes down. What it still catches is what
// both halves of #632 were — a query that DISCOVERS references, keyed on
// something other than the referenced record itself.
func boundToCallerArgument(sql, table string) bool {
	return regexp.MustCompile(
		`\b(?:[A-Za-z_][\w]*\.)?` + table + `_id\s*(?:=|IN)\s*(?:ANY\s*\(\s*)?\$\d+`).MatchString(sql)
}

// selectSpan locates one SELECT: [from, to) is its projection, and [to, stop)
// the rest of its own query — the clauses that narrow rather than answer.
type selectSpan struct{ from, to, stop int }

// selectLists returns a span for every SELECT in the statement. The projection
// runs from just after the keyword to the FROM that closes it at the same paren
// depth; the query itself runs on to the paren that encloses it, or to the end.
//
// Nested selects are found on their own pass as well as inside an outer span,
// which is right rather than redundant: a scalar subquery in a projection hands
// its column back through the outer row, while its own predicates do not.
func selectLists(sql string) []selectSpan {
	var lists []selectSpan
	for _, start := range keywordOffsets(sql, "SELECT") {
		from := start + len("SELECT")
		depth, to, stop := 0, -1, len(sql)
		for i := from; i < len(sql); i++ {
			switch sql[i] {
			case '(':
				depth++
				continue
			case ')':
				if depth == 0 {
					// This select is a subquery, and its enclosing paren is
					// where everything it can say about itself ends.
					stop = i
					i = len(sql)
					continue
				}
				depth--
				continue
			}
			if depth == 0 && to < 0 && isKeywordAt(sql, i, "FROM") {
				to = i
			}
		}
		if to < 0 || to > stop {
			// No FROM of its own: the whole thing is projection.
			to = stop
		}
		lists = append(lists, selectSpan{from: from, to: to, stop: stop})
	}
	return lists
}

// keywordOffsets finds each word-bounded occurrence of an uppercase SQL
// keyword. This tree writes its SQL keywords uppercase and its identifiers
// lowercase, which is what lets the match stay case-sensitive and so never
// mistake a column named `from` for the clause.
func keywordOffsets(sql, keyword string) []int {
	var at []int
	for i := 0; i+len(keyword) <= len(sql); i++ {
		if isKeywordAt(sql, i, keyword) {
			at = append(at, i)
		}
	}
	return at
}

func isKeywordAt(sql string, i int, keyword string) bool {
	if i+len(keyword) > len(sql) || sql[i:i+len(keyword)] != keyword {
		return false
	}
	if i > 0 && isSQLWordByte(sql[i-1]) {
		return false
	}
	end := i + len(keyword)
	return end == len(sql) || !isSQLWordByte(sql[end])
}

func isSQLWordByte(b byte) bool {
	return b == '_' || ('0' <= b && b <= '9') || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

// tierFile is one swept source file plus the offsets to report positions in.
type tierFile struct {
	Path string
	File *ast.File
	fset *token.FileSet
}

func (p tierFile) Line(pos token.Pos) int { return p.fset.Position(pos).Line }

// tierFiles parses every production Go file under one subtree. Test and
// integration-tagged sources are excluded for the reason the other gates
// exclude them: the obligation binds code that can reach a shipped binary.
func tierFiles(t *testing.T, root string) []tierFile {
	t.Helper()
	fset := token.NewFileSet()
	var files []tierFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			return err
		}
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		files = append(files, tierFile{Path: filepath.ToSlash(path), File: parsed, fset: fset})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no production Go files — teach this gate where the tier moved", root)
	}
	return files
}
