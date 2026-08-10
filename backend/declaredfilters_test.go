// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A declared narrowing parameter binds the read it is declared on, or it is not
// declared.
//
// The defect this closes is the widest wrong answer there is. `GET
// /v1/people?tag=vip` whose handler never reads `tag` returns EVERY person the
// caller may see, with 200 OK and a well-formed page: the client cannot tell
// that from a workspace where everyone carries the tag. Three of these were
// live when the gate was written — `tag`, `organization.domain`,
// `activity.assignee_id` — plus one shadow, `deal.project_id`, that answered
// the whole incumbent mirror to a caller who named one project.
//
// It is derived rather than listed. The generated contract carries every list
// operation's query parameters as a `*Params` struct, so the census IS the
// contract: a parameter added upstream lands here as a failure until a handler
// reads it. That is the half a hand-maintained list cannot do, and the half
// that matters — every one of these defects was a parameter someone declared
// and nobody came back for.
//
// Two obligations, because a request in overlay mode meets two handlers:
//
//   - the NATIVE one must reach its store with the dial (or hand the whole
//     params value on to something that does, or own the raw query string
//     itself — the report handlers parse it wholesale);
//   - the overlay SHADOW must name the dial in the branch that reads the
//     mirror: forward it, or refuse it with the 422 that says the mirror cannot
//     answer this. Delegating to the native handler is the OTHER branch, so a
//     reference inside that closure proves nothing about the mirror path. This
//     is the fitness function #579 asks for, and `occurred_from`/`occurred_to`
//     — forwarded by neither and refused by neither, so a whole mirrored
//     timeline came back as though it were the requested day — is the failure
//     it is derived from.
//
// Paging and ordering are deliberately OUT of scope: `cursor`, `limit` and
// `sort` are the shared components a page's SHAPE is spelled with, not its
// membership, and a handler that ignores one answers a differently-shaped page
// rather than a different question. Six operations ignore one of the three
// today; that is its own obligation with its own fix, filed separately.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

const (
	// contractParamsSource holds the generated `*Params` structs — the census
	// of what the contract declares a read may be narrowed by.
	contractParamsSource = "internal/contracts/api_gen.go"

	// overlayModeDispatch is the method every read shadow opens with — "does
	// this request read the mirror?" — and therefore what identifies one
	// without naming the file it happens to live in.
	overlayModeDispatch = "overlayReadMode"

	// contractsPackageAlias is how every handler spells the generated contract
	// package, which is what makes a params type recognisable in a signature.
	contractsPackageAlias = "crmcontracts"

	// wantMinimumNarrowedOperations guards the way this gate fails silently: a
	// census that stops recognising the generated shape finds no parameters,
	// judges nothing, and reads exactly like a clean tree. Fifty operations
	// declare a narrowing parameter today; the floor sits well below that, so
	// retiring one stays an ordinary change and only a collapse is a finding.
	wantMinimumNarrowedOperations = 30

	// wantMinimumOverlayShadows is the same guard for the shadow walk: six
	// shadows carry narrowing dials today.
	wantMinimumOverlayShadows = 4
)

// pagingParameterTypes are the generated types the contract spells its shared
// paging and ordering parameters with. Recognising them by TYPE rather than by
// name is what keeps the exclusion honest: it is the contract's own component
// reuse that says these three are a page's shape, and a filter that happened to
// be called `sort` on some operation would still be judged.
var pagingParameterTypes = map[string]bool{"Cursor": true, "Limit": true, "Sort": true}

// declaredFilter is one narrowing query parameter of one operation: the Go
// field a handler reads, and the name a caller types.
type declaredFilter struct {
	field string
	wire  string
}

// overlayDialsTheMirrorAnswersTheSameWayEitherWay ratifies a dial an overlay
// shadow neither forwards nor refuses. An entry here says the mirror answers
// the same page with the parameter as without it — which is a property of the
// mirror, so it is keyed by the parameter rather than by each shadow.
var overlayDialsTheMirrorAnswersTheSameWayEitherWay = gatekit.Waive(map[string]string{
	"include_archived": "the mirror holds no archived rows at all — a tombstoned incumbent record is deleted " +
		"there rather than archived — so both values of this dial answer the same page, and refusing it " +
		"would cost every caller that sends the parameter's own default a 422 for nothing",
})

// TestEveryDeclaredNarrowingParameterReachesItsRead is the native obligation.
func TestEveryDeclaredNarrowingParameterReachesItsRead(t *testing.T) {
	declared := narrowingParametersByType(t)
	if len(declared) < wantMinimumNarrowedOperations {
		t.Fatalf("only %d operations declare a narrowing query parameter in %s, want at least %d — "+
			"the census is reading the wrong shape and would judge nothing",
			len(declared), contractParamsSource, wantMinimumNarrowedOperations)
	}

	handlers := 0
	for _, file := range declaredFilterScope(declared).Files(t) {
		for _, h := range paramsHandlersIn(file, declared, descendIntoClosures) {
			handlers++
			for _, missed := range h.unread {
				t.Errorf("%s: %s declares `%s` and %s never reads it, so the page it answers is wider "+
					"than the one that was asked for — bind the parameter to the read, or retire it "+
					"from the contract",
					file.Path, h.paramsType, missed, h.name)
			}
		}
	}
	if handlers == 0 {
		t.Fatal("no handler taking a narrowing params value was found: the walk is judging nothing")
	}
}

// TestEveryDeclaredNarrowingParameterIsForwardedOrRefusedByItsOverlayShadow is
// the second obligation — #579's fitness function.
func TestEveryDeclaredNarrowingParameterIsForwardedOrRefusedByItsOverlayShadow(t *testing.T) {
	declared := narrowingParametersByType(t)
	shadows := 0
	for _, file := range overlayShadowScope().Files(t) {
		for _, shadow := range paramsHandlersIn(file, declared, skipClosures) {
			shadows++
			for _, missed := range shadow.unread {
				if overlayDialsTheMirrorAnswersTheSameWayEitherWay.Waived(t, missed) {
					continue
				}
				t.Errorf("%s: %s neither forwards `%s` to the mirror nor refuses it, so in overlay mode the "+
					"dial is dropped and the whole mirrored set comes back as though it had been applied — "+
					"add it to the shadow's refused parameters, or pass it through",
					file.Path, shadow.name, missed)
			}
		}
	}
	if shadows < wantMinimumOverlayShadows {
		t.Fatalf("the walk found %d read shadows taking a narrowing params value, want at least %d — it is "+
			"reading the wrong shape and would judge nothing", shadows, wantMinimumOverlayShadows)
	}
	overlayDialsTheMirrorAnswersTheSameWayEitherWay.AssertAllMatched(t)
}

// overlayShadowScope finds the read shadows by what they DO — dispatch on
// overlay mode — rather than by the file they sit in today.
//
// The native obligation proves its roots with a Scope for a stated reason: a
// root naming the wrong tree finds nothing objectionable and reads exactly
// like a clean one. Naming the shadow file directly would have made this half
// the same unproven claim, one obligation apart: a shadow added in another
// compose file would be judged by nobody, and its dropped dial would look
// exactly like no dropped dial.
func overlayShadowScope() gatekit.Scope {
	return gatekit.Scope{
		Roots:   []string{composeTier},
		Subject: func(_ string, file *ast.File) bool { return callsOverlayModeDispatch(file) },
	}
}

// callsOverlayModeDispatch reports whether a file holds a read shadow: a
// function that asks whether this request reads the mirror. Every shadow
// begins that way, and nothing else in the tier does.
func callsOverlayModeDispatch(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if method, isMethod := call.Fun.(*ast.SelectorExpr); isMethod && method.Sel.Name == overlayModeDispatch {
			found = true
		}
		return !found
	})
	return found
}

// TestTheDeclaredFilterWalkReportsADroppedParameterAndNothingElse is the gate's
// own defect test. Both obligations above pass today, and a walk that reported
// nothing because it recognises nothing would pass identically — so each shape
// the walk must tell apart is put to it here, against source written for the
// purpose rather than against the tree it judges.
func TestTheDeclaredFilterWalkReportsADroppedParameterAndNothingElse(t *testing.T) {
	declared := map[string][]declaredFilter{"ListThingsParams": {{field: "Tag", wire: "tag"}}}
	for _, probe := range []struct {
		name   string
		policy closurePolicy
		body   string
		unread []string
	}{
		{
			"a dropped parameter is reported", descendIntoClosures,
			`in := Input{Limit: params.Limit}; use(in)`,
			[]string{"tag"},
		},
		{
			"a parameter the handler reads is not", descendIntoClosures,
			`use(Input{Tag: params.Tag})`, nil,
		},
		{
			"a params value handed on whole is not", descendIntoClosures,
			`h.other(w, r, params)`, nil,
		},
		{
			"a handler owning the raw query string is not", descendIntoClosures,
			`use(parse(r.URL.Query()))`, nil,
		},
		{
			"a shadow that names the dial only in its delegation closure is reported", skipClosures,
			`shadow(func() { h.native(w, r, params) })`,
			[]string{"tag"},
		},
		{
			// The search shadow's shape: it delegates with a plain call rather
			// than through a closure, so it is the WHOLESALE allowance — not
			// the closure rule — that would exempt it from the obligation, and
			// it would be exempted from all of it at once.
			"a shadow that delegates without a closure is judged all the same", skipClosures,
			`if !overlay { h.native(w, r, params); return }; mirror(params.Q)`,
			[]string{"tag"},
		},
		{
			"a shadow that names the dial in the mirror branch is not", skipClosures,
			`shadow(func() { h.native(w, r, params) }, refuse{params.Tag != nil})`, nil,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			file := parseGoSource(t, "package p\nfunc (h H) ListThings(w W, r R, params crmcontracts.ListThingsParams) {\n"+
				probe.body+"\n}\n")
			handlers := paramsHandlersIn(file, declared, probe.policy)
			if len(handlers) != 1 {
				t.Fatalf("the walk found %d handlers, want 1 — it is not recognising the signature at all", len(handlers))
			}
			if got := strings.Join(handlers[0].unread, ","); got != strings.Join(probe.unread, ",") {
				t.Errorf("unread = %q, want %q", got, strings.Join(probe.unread, ","))
			}
		})
	}
}

// TestTheDeclaredFilterCensusReadsTheGeneratedShape pins the other half of the
// silent failure: a census that mistook the paging components for filters, or
// missed a form tag, would judge the wrong set without ever reporting it.
func TestTheDeclaredFilterCensusReadsTheGeneratedShape(t *testing.T) {
	declared := narrowingParametersByType(t)
	people, listed := declared["ListPeopleParams"]
	if !listed {
		t.Fatalf("the census carries no ListPeopleParams — it is reading the wrong shape")
	}
	var wire []string
	for _, filter := range people {
		wire = append(wire, filter.wire)
	}
	// The person list declares exactly these, and the three paging components
	// it also declares are not among them.
	want := "ai_written,captured_by_kind,include_archived,owner_id,q,tag"
	if got := strings.Join(wire, ","); got != want {
		t.Errorf("listPeople's narrowing parameters = %q, want %q", got, want)
	}
}

// declaredFilterScope proves the roots hold every handler this gate judges: a
// params value taken anywhere else would be a read the gate never sees.
func declaredFilterScope(declared map[string][]declaredFilter) gatekit.Scope {
	return gatekit.Scope{
		Roots: []string{modulesDir, composeTier},
		Subject: func(_ string, file *ast.File) bool {
			return len(paramsHandlersIn(gatekit.ParsedFile{File: file}, declared, descendIntoClosures)) > 0
		},
	}
}

// paramsHandler is one handler judged: which operation's params it takes, and
// which of that operation's narrowing parameters it never reads.
type paramsHandler struct {
	name       string
	paramsType string
	unread     []string
}

// closurePolicy says whether a reference inside a function literal counts.
type closurePolicy bool

const (
	// descendIntoClosures counts a reference wherever it stands: a native
	// handler that maps its parameters inside a callback has still read them.
	descendIntoClosures closurePolicy = true
	// skipClosures counts only references in the function's own body. An
	// overlay shadow's closure IS the branch that delegates to the native
	// handler, so a reference inside it says nothing about the mirror branch.
	skipClosures closurePolicy = false
)

// paramsHandlersIn walks one file for handlers taking a declared params value
// and reports what each leaves unread, by wire name and in declaration order.
func paramsHandlersIn(file gatekit.ParsedFile, declared map[string][]declaredFilter, policy closurePolicy) []paramsHandler {
	var out []paramsHandler
	for _, decl := range file.File.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ident, paramsType := paramsArgument(fn, declared)
		if paramsType == "" {
			continue
		}
		read, wholesale := parameterReferences(fn.Body, ident, policy)
		var unread []string
		if !wholesale {
			for _, filter := range declared[paramsType] {
				if !read[filter.field] {
					unread = append(unread, filter.wire)
				}
			}
		}
		out = append(out, paramsHandler{name: fn.Name.Name, paramsType: paramsType, unread: unread})
	}
	return out
}

// paramsArgument finds the handler's params argument: the identifier it is
// bound to (empty for `_`, which reads nothing at all) and the generated type
// it carries. A signature naming no declared params type answers "".
func paramsArgument(fn *ast.FuncDecl, declared map[string][]declaredFilter) (ident, paramsType string) {
	for _, field := range fn.Type.Params.List {
		selector, ok := field.Type.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != contractsPackageAlias || len(declared[selector.Sel.Name]) == 0 {
			continue
		}
		if len(field.Names) == 1 && field.Names[0].Name != "_" {
			return field.Names[0].Name, selector.Sel.Name
		}
		return "", selector.Sel.Name
	}
	return "", ""
}

// parameterReferences reports which fields of the params value the body reads
// by name, and whether it binds the value WHOLESALE — in which case the field
// set says nothing and the handler is accounted for either way.
//
// The two wholesale shapes exist because binding a parameter is not always a
// selector at the handler: a body that hands the whole params value on has
// passed every field to whatever maps them, and a body that parses the raw
// query string owns every parameter in it — which is how the report handlers
// read the reserved `by`/`agg` keys alongside a free-form vocabulary the
// generated struct cannot spell. Both are broader than their justification:
// ANY call taking the value counts, so a debug log naming `params` would
// exempt a handler. They are the native obligation's allowances and they are
// stated here rather than narrowed, because a walk that guessed which calls
// "really" bind would be the unreliable half of this gate.
//
// Neither is granted to a SHADOW. Handing the params value on is what an
// overlay shadow does to reach the native handler — its OTHER branch — so
// accepting it there would exempt every shadow from the obligation by the
// shape they all share. The Search shadow proved this the direct way: it
// delegates with a plain call rather than through a closure, so under the
// closure rule alone it was judged on nothing at all.
func parameterReferences(body *ast.BlockStmt, ident string, policy closurePolicy) (read map[string]bool, wholesale bool) {
	// A handler that discards the generated struct (`_ crmcontracts.XParams`)
	// reads no field by name, but it may still own the raw query string — so
	// the walk runs either way, and only the by-name half depends on there
	// being an identifier to match.
	read = map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isClosure := n.(*ast.FuncLit); isClosure && policy == skipClosures {
			return false
		}
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := node.X.(*ast.Ident); ok && x.Name == ident {
				read[node.Sel.Name] = true
			}
		case *ast.CallExpr:
			if policy == descendIntoClosures {
				wholesale = wholesale || readsRawQuery(node) || passesWhole(node, ident)
			}
		}
		return true
	})
	return read, wholesale
}

// readsRawQuery reports whether the call is r.URL.Query(), the handler taking
// the whole query string rather than the generated struct.
func readsRawQuery(call *ast.CallExpr) bool {
	method, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || method.Sel.Name != "Query" {
		return false
	}
	url, ok := method.X.(*ast.SelectorExpr)
	return ok && url.Sel.Name == "URL"
}

// passesWhole reports whether the call hands the params value on unopened.
func passesWhole(call *ast.CallExpr, ident string) bool {
	for _, arg := range call.Args {
		if named, ok := arg.(*ast.Ident); ok && named.Name == ident {
			return true
		}
	}
	return false
}

// narrowingParametersByType is the census: every generated `*Params` struct,
// keyed by type name, carrying the query parameters that narrow what the
// operation answers. Paging and ordering are dropped by type, and a struct left
// with nothing is dropped with them.
func narrowingParametersByType(t *testing.T) map[string][]declaredFilter {
	t.Helper()
	out := map[string][]declaredFilter{}
	for _, decl := range parseGoFile(t, contractParamsSource).File.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(typeSpec.Name.Name, "Params") {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			if filters := narrowingFields(structType); len(filters) > 0 {
				out[typeSpec.Name.Name] = filters
			}
		}
	}
	return out
}

// narrowingFields reads one params struct's query fields: the `form` tag names
// the wire parameter, and a field typed by one of the shared paging components
// is not one of them.
func narrowingFields(structType *ast.StructType) []declaredFilter {
	var out []declaredFilter
	for _, field := range structType.Fields.List {
		if len(field.Names) != 1 || field.Tag == nil {
			continue
		}
		wire := formTagName(field.Tag.Value)
		if wire == "" || pagingParameterTypes[pointedToTypeName(field.Type)] {
			continue
		}
		out = append(out, declaredFilter{field: field.Names[0].Name, wire: wire})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].wire < out[j].wire })
	return out
}

// formTagName reads the query-parameter name out of a struct tag, or "" when
// the field carries none — a header or path parameter, which this gate is not
// about.
func formTagName(tag string) string {
	const marker = `form:"`
	start := strings.Index(tag, marker)
	if start < 0 {
		return ""
	}
	rest := tag[start+len(marker):]
	end := strings.IndexAny(rest, `",`)
	if end <= 0 {
		return ""
	}
	return rest[:end]
}

// pointedToTypeName names the type a field holds, through the pointer every
// optional parameter is generated as, and "" for anything qualified by a
// package — the shared paging components are all local to the generated file.
func pointedToTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if named, ok := expr.(*ast.Ident); ok {
		return named.Name
	}
	return ""
}

// parseGoFile parses one source of the backend module, relative to its root.
func parseGoFile(t *testing.T, rel string) gatekit.ParsedFile {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	return gatekit.ParsedFile{Path: rel, File: file}
}

// parseGoSource parses source written for the walk's own defect test.
func parseGoSource(t *testing.T, src string) gatekit.ParsedFile {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the probe source: %v", err)
	}
	return gatekit.ParsedFile{Path: "probe.go", File: file}
}
