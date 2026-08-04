// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The census over this repo's own gate machinery: a gate's exceptions are held
// to the standard the gate holds its subjects to.
//
// gatekit gives a waiver two obligations — state what it costs, and describe
// code that still exists — and enforces both. A map declared beside a gate
// instead of inside gatekit has neither: it reads as a ratified exception while
// certifying nothing, and it is one `var` away from every gate in the tree. The
// three rules here derive the population from the tree rather than tracking it
// as a list, so the next declaration inherits the obligation:
//
//   - a package-level map from subject to reason, in a test file, is either a
//     gatekit.Waivers set or a declared fixture;
//   - every waiver set is swept for staleness from exactly one place;
//   - nothing but a test reaches the waiver machinery at all.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// backendTree is the census universe: every Go file this module and the tool
	// module under it declare, whichever tier they sit in.
	backendTree = "."

	// gatekitImportPath is the waiver machinery every rule here is about.
	gatekitImportPath = "github.com/gradionhq/margince/backend/internal/shared/gatekit"

	// fixtureMarker declares that a package-level reason map is deliberately NOT
	// a waiver, and the text after it says what the map is instead — expected
	// data a test compares against, or state the test's own walk writes. The
	// marker classifies; it never excuses, which is why the text is required: a
	// bare marker would be the reasonless waiver this census exists to refuse.
	fixtureMarker = "gatekit:fixture"

	// wantFixtureAnnotations pins how many declarations carry the marker.
	// Permitting the marker without counting it would let it spread quietly and
	// reopen the class these rules close; pinned, every new one moves a number a
	// reviewer sees.
	wantFixtureAnnotations = 9
)

// reasonMap is one package-level declaration mapping subjects to strings — the
// population rules 1 and 2 are derived over.
type reasonMap struct {
	path   string
	pkg    string
	line   int
	name   string
	waived bool // constructed through gatekit.Waive
}

// A package-level map from subject to reason is a waiver or a declared fixture.
//
// Both halves of a waiver's contract live in gatekit: a reason too short to
// state a cost fails where the entry is relied on, and an entry no subject
// reached is reported stale. A bare map beside a gate has neither, so it keeps
// reading as ratification long after the code it named is gone. The alternative
// is not "no map" — some of these maps ARE the assertion — but saying which,
// once, at the declaration.
func TestEveryPackageLevelReasonMapIsAWaiverOrADeclaredFixture(t *testing.T) {
	files, fset := censusFiles(t, "test source", isTestSource)
	declared, annotated, written := 0, 0, 0
	for _, pf := range files {
		markers, commented := fixtureMarkers(pf, fset)
		written += len(markers)
		for _, decl := range reasonMaps(t, pf, fset) {
			declared++
			reason, marked := fixtureReason(markers, commented, decl.line)
			switch {
			case decl.waived && marked:
				t.Errorf("%s:%d: %s is a waiver set and also carries a %s marker: the marker says a map "+
					"is not a waiver, so on a waiver it contradicts the declaration — drop it",
					decl.path, decl.line, decl.name, fixtureMarker)
			case decl.waived:
			case !marked:
				t.Errorf("%s:%d: %s maps subjects to reason strings but is neither a waiver nor a declared "+
					"fixture: wrap it in gatekit.Waive so its reasons are held to a standard and its stale "+
					"entries reported, or, if the values are expected data rather than costs, say so with a "+
					"`// %s <what it is>` line on the declaration",
					decl.path, decl.line, decl.name, fixtureMarker)
			case reason == "":
				annotated++
				t.Errorf("%s:%d: %s carries a bare %s marker: the marker only says the map is not a waiver, "+
					"so without the text saying what it IS, it is the reasonless exception this census refuses",
					decl.path, decl.line, decl.name, fixtureMarker)
			default:
				annotated++
			}
		}
	}
	if declared == 0 {
		t.Fatalf("no package-level map-to-string declaration found in %d test sources — the walk enumerates "+
			"nothing, and a census that matches nothing certifies the whole tree", len(files))
	}
	if annotated != wantFixtureAnnotations {
		t.Errorf("%d declarations are marked %s, pinned at %d: a fixture is a classification made once, so "+
			"move the pin in the same change that adds or removes one",
			annotated, fixtureMarker, wantFixtureAnnotations)
	}
	if written != annotated {
		t.Errorf("%d %s markers are written but %d sit on a declaration this census enumerates: a marker "+
			"anywhere else classifies nothing — put it on the declaration's own comment block, with no blank "+
			"line between", written, fixtureMarker, annotated)
	}
}

// Every waiver set is swept for staleness from exactly one place.
//
// AssertAllMatched is what turns a waiver from a permanent grant into one that
// has to keep describing live code, and gatekit cannot call it for you: zero
// call sites makes that sweep opt-in, which is the same as absent. Two are no
// better than zero — matched accumulates across every test in the package, so
// whichever runs first sees the other's subjects unreached and reports staleness
// that is not there.
func TestEveryWaiversDeclarationIsSweptForStalenessExactlyOnce(t *testing.T) {
	files, fset := censusFiles(t, "Go source", func(string) bool { return true })

	// A package, not a directory: a waiver set is unexported, so only the
	// package that declares it can name it in a sweep.
	type pkgKey struct{ dir, pkg string }
	sweeps := map[pkgKey]map[string]int{}
	var declared []reasonMap
	for _, pf := range files {
		key := pkgKey{dir: path.Dir(pf.path), pkg: pf.file.Name.Name}
		if sweeps[key] == nil {
			sweeps[key] = map[string]int{}
		}
		for receiver, count := range assertAllMatchedSites(pf) {
			sweeps[key][receiver] += count
		}
		for _, decl := range reasonMaps(t, pf, fset) {
			if decl.waived {
				declared = append(declared, decl)
			}
		}
	}
	if len(declared) == 0 {
		t.Fatalf("no gatekit.Waive declaration found in %d Go sources — the walk enumerates nothing, and a "+
			"census that matches nothing certifies the whole tree", len(files))
	}
	for _, decl := range declared {
		switch sweeps[pkgKey{path.Dir(decl.path), decl.pkg}][decl.name] {
		case 1:
		case 0:
			t.Errorf("%s:%d: nothing calls %s.AssertAllMatched: an entry that matched no subject then reads "+
				"as ratification of code that is gone — call it once, from the test that walks the full "+
				"subject set", decl.path, decl.line, decl.name)
		default:
			t.Errorf("%s:%d: %s.AssertAllMatched is called from more than one place in package %s: matches "+
				"accumulate across the package's tests, so whichever asserts first reports staleness that is "+
				"not there — leave the call to the test that walks the full subject set",
				decl.path, decl.line, decl.name, decl.pkg)
		}
	}
}

// gatekit serves tests only.
//
// gatekit is test infrastructure living in internal/shared, a tier that
// otherwise holds the product's Tier-0 domain leaves. That placement is safe
// only while product code cannot reach it: a waiver mechanism callable from a
// store would be a way to mark shipped behaviour "ratified", which is the one
// thing a waiver must not be able to do.
//
// The walk covers every non-test Go file in the backend tree — internal/, cmd/,
// pkg/, migrations/ and the tools module — which is the whole population able to
// reach gatekit at all. The workspace's other modules (composition, cli/craft,
// extensions/*) sit outside the github.com/gradionhq/margince/backend/ prefix,
// so the toolchain's internal-package rule refuses them the import before this
// walk would have to.
func TestGatekitServesTestsOnly(t *testing.T) {
	files, _ := censusFiles(t, "product source", func(p string) bool { return !isTestSource(p) })
	for _, pf := range files {
		for _, imported := range pf.file.Imports {
			if importPath(t, pf.path, imported) != gatekitImportPath {
				continue
			}
			t.Errorf("%s imports gatekit from product code: a gate's exceptions must not be reachable from "+
				"the code they would exempt, or a shipped path could ratify itself", pf.path)
		}
	}
}

// censusFiles parses every Go file under the backend tree that keep admits,
// comments included, and fails when it admits none: each rule proves its own
// walk finds something, because a walk that matches nothing reports green over
// whatever it was pointed away from.
//
// Files are parsed one at a time rather than loaded as packages so that a build
// constraint cannot hide one — the integration-tagged suites hold waivers too,
// and they are exactly the ones an untagged build never compiles.
func censusFiles(t *testing.T, what string, keep func(path string) bool) ([]parsedFile, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	var out []parsedFile
	err := filepath.WalkDir(backendTree, func(walked string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel := filepath.ToSlash(walked)
		if !strings.HasSuffix(rel, ".go") || !keep(rel) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, walked, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		out = append(out, parsedFile{path: rel, file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for every %s: %v", backendTree, what, err)
	}
	if len(out) == 0 {
		t.Fatalf("no %s found under %s — the census is reading the wrong tree", what, backendTree)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, fset
}

// isTestSource reports whether the path is a Go test file — the only tier the
// waiver machinery belongs to.
func isTestSource(path string) bool { return strings.HasSuffix(path, "_test.go") }

// reasonMaps returns every package-level declaration in the file whose value
// type is string, keyed by any type at all.
//
// The walk is over the file's declarations, not over its text: a declaration
// inside a `var ( … )` group is invisible to a pattern anchored on `var`. The
// key is deliberately unconstrained — a waiver is keyed by the vocabulary its
// gate draws subjects from, a record type or a table name as readily as a
// string, and a rule that only saw map[string]string would sail past the typed
// ones while typed keys are the direction gatekit pushes.
func reasonMaps(t *testing.T, pf parsedFile, fset *token.FileSet) []reasonMap {
	t.Helper()
	waive := gatekitName(t, pf)
	var out []reasonMap
	for _, decl := range pf.file.Decls {
		gen, isGen := decl.(*ast.GenDecl)
		if !isGen || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}
			mapped, waived := stringValuedMap(value, waive)
			if !mapped {
				continue
			}
			for _, name := range value.Names {
				out = append(out, reasonMap{
					path:   pf.path,
					pkg:    pf.file.Name.Name,
					line:   fset.Position(value.Pos()).Line,
					name:   name.Name,
					waived: waived,
				})
			}
		}
	}
	return out
}

// stringValuedMap reports whether the spec declares a map with string values,
// and whether gatekit.Waive constructs it. The three shapes such a declaration
// takes are the bare literal, the type with no value, and the literal handed to
// Waive.
func stringValuedMap(spec *ast.ValueSpec, waive string) (mapped, waived bool) {
	if isStringValuedMapType(spec.Type) {
		return true, false
	}
	for _, value := range spec.Values {
		switch expr := value.(type) {
		case *ast.CompositeLit:
			if isStringValuedMapType(expr.Type) {
				return true, false
			}
		case *ast.CallExpr:
			if !isWaiveCall(expr.Fun, waive) {
				continue
			}
			for _, arg := range expr.Args {
				literal, isLiteral := arg.(*ast.CompositeLit)
				if isLiteral && isStringValuedMapType(literal.Type) {
					return true, true
				}
			}
		}
	}
	return false, false
}

// isStringValuedMapType reports whether the expression is a map type whose
// values are strings — the reason side of a subject-to-reason map.
func isStringValuedMapType(expr ast.Expr) bool {
	mapType, isMap := expr.(*ast.MapType)
	if !isMap {
		return false
	}
	value, isIdent := mapType.Value.(*ast.Ident)
	return isIdent && value.Name == "string"
}

// isWaiveCall reports whether the callee is gatekit.Waive, named through the
// local name the file imports gatekit under, or unqualified inside gatekit's own
// package.
func isWaiveCall(callee ast.Expr, waive string) bool {
	if waive == "" {
		return false
	}
	if bare, isIdent := callee.(*ast.Ident); isIdent {
		return waive == "." && bare.Name == "Waive"
	}
	selector, isSelector := callee.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != "Waive" {
		return false
	}
	qualifier, isIdent := selector.X.(*ast.Ident)
	return isIdent && qualifier.Name == waive
}

// gatekitName is the local name the file imports gatekit under, "." inside
// gatekit's own package, and "" when the file cannot name Waive at all.
// Resolving the name keeps an aliased import inside the rule instead of
// silently reclassifying its waivers as bare maps.
func gatekitName(t *testing.T, pf parsedFile) string {
	t.Helper()
	if pf.file.Name.Name == "gatekit" {
		return "."
	}
	for _, imported := range pf.file.Imports {
		if importPath(t, pf.path, imported) != gatekitImportPath {
			continue
		}
		if imported.Name != nil {
			return imported.Name.Name
		}
		return "gatekit"
	}
	return ""
}

// importPath is the unquoted path of an import spec.
func importPath(t *testing.T, file string, imported *ast.ImportSpec) string {
	t.Helper()
	unquoted, err := strconv.Unquote(imported.Path.Value)
	if err != nil {
		t.Fatalf("%s: import path %s does not unquote: %v", file, imported.Path.Value, err)
	}
	return unquoted
}

// fixtureMarkers indexes every fixture marker in the file by the line it sits
// on, mapped to the text after it, alongside the line of every comment so a
// marker can be tied to the declaration below it.
//
// Text is read raw off each comment, as cli/craft reads its own //craft:ignore
// directives. Go treats any comment matching //<word>:<word> as a directive and
// CommentGroup.Text() drops directives, so a marker read through Text() is
// invisible to the census that requires it — the space after the slashes keeps
// the marker readable to a person, not findable to this walk.
func fixtureMarkers(pf parsedFile, fset *token.FileSet) (markers map[int]string, commented map[int]bool) {
	markers = map[int]string{}
	commented = map[int]bool{}
	for _, group := range pf.file.Comments {
		for _, comment := range group.List {
			line := fset.Position(comment.Slash).Line
			commented[line] = true
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(comment.Text, "//"), "/*"))
			if !strings.HasPrefix(text, fixtureMarker) {
				continue
			}
			reason := strings.TrimSuffix(strings.TrimSpace(text[len(fixtureMarker):]), "*/")
			markers[line] = strings.TrimSpace(reason)
		}
	}
	return markers, commented
}

// fixtureReason returns the reason from the marker in the comment block directly
// above line, and whether that block carries one at all. Adjacency is what binds
// a marker to a declaration: a marker one blank line away annotates nothing, and
// a marker further up the file annotates whatever it does sit above.
func fixtureReason(markers map[int]string, commented map[int]bool, line int) (string, bool) {
	for above := line - 1; commented[above]; above-- {
		if reason, marked := markers[above]; marked {
			return reason, true
		}
	}
	return "", false
}

// assertAllMatchedSites counts, per receiver name, the AssertAllMatched calls in
// the file.
func assertAllMatchedSites(pf parsedFile) map[string]int {
	sites := map[string]int{}
	ast.Inspect(pf.file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector || selector.Sel.Name != "AssertAllMatched" {
			return true
		}
		receiver, isIdent := selector.X.(*ast.Ident)
		if !isIdent {
			return true
		}
		sites[receiver.Name]++
		return true
	})
	return sites
}
