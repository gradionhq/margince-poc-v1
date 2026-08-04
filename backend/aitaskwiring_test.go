// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The census says a site EXISTS; this says a process role runs it.
//
// Those are two claims, and only the first one had a gate. A site can be
// registered, bound to a certification case, scored by the corpus and written
// into a record while no binary ever reaches it — the case drives the builder
// directly, so the whole certification arc stays green over code that has
// fallen out of the product. That is the same "certified but not shipped" lie
// the census exists to kill, one level up from where the census sees.
//
// The link this reads is the model path lane. Every model call in this build
// rides one, a lane is named for the ai.Task it serves (modellanes_test.go
// gates both halves of that: the name against the contract, the binding
// against the name), and a process role has to hand the lane to the wiring
// that uses it. So a censused site's task must own a lane, and some role under
// cmd/ must pass that lane to something. Delete the compose.WithOfferDraft
// line from cmd/api and OfferDraft loses its last reader — which is the
// failure this exists to make loud.
//
// WHAT IT CANNOT SEE, stated rather than implied. It stops at the lane. It
// does not follow the option into the server, the server into the handler, or
// the handler into the request the certification case scores — so it catches
// wiring REMOVED, not wiring rerouted: a lane handed to a compose function
// that no longer serves the site still satisfies it. Following the rest would
// need a whole-program call graph rooted at each main, which this module
// cannot build from a test without a package-loading dependency it does not
// carry. A narrower check that is true is worth more than a broad one that
// only looks like it reads the wiring.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// composeImportPath is the package whose ModelPath the roles hand around.
const composeImportPath = modulePath + "/internal/compose"

// laneWiringExemptions names each censused task that owns no model path lane,
// with the reason it owns none. An exemption without a reason is itself a
// finding: the list costs an explanation to grow, which is what keeps it from
// becoming the place unwired sites go to be forgotten.
//
// The key is the contract's task name rather than its ai.Task constant because
// the root component may not import the ai module (.go-arch-lint.yml). A task
// renamed upstream therefore stops matching its exemption — and lands on the
// "owns no lane" error asking for one, which is the direction a stale
// exemption should fail in.
var laneWiringExemptions = gatekit.Waive(map[string]string{
	"cert_judge": "the rubric judge is built by the certification runner on its own pinned binding (aicert/runner.go), never by a process role — a judge on the candidate's lane would let a model grade itself",
})

func TestEveryCensusedSiteRidesALaneAProcessRoleWires(t *testing.T) {
	defer laneWiringExemptions.AssertAllMatched(t)
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	wired := lanesWiredByProcessRoles(t)
	if len(wired) == 0 {
		t.Fatal("no process role reads a single model path lane — the scan found nothing, so it can prove nothing")
	}

	for _, site := range census.All() {
		task := string(site.Task)
		lane, hasLane := laneNameFor(task)
		if laneWiringExemptions.Waived(t, task) {
			if hasLane {
				t.Errorf("site %s/%s is exempted as laneless, but ModelPath.%s exists — drop the exemption and let the rule hold it", site.Task, site.Variant, lane)
			}
			continue
		}
		if !hasLane {
			t.Errorf("site %s/%s is censused but task %q owns no ModelPath lane, so no process role can serve it — add the lane, or record in laneWiringExemptions why the site runs outside every role",
				site.Task, site.Variant, task)
			continue
		}
		if !wired[lane] {
			t.Errorf("site %s/%s rides ModelPath.%s, which no process role under cmd/ hands to anything — the site is certifiable but unshipped; wire the lane into a role's options, or drop the site from the contract and the census",
				site.Task, site.Variant, lane)
		}
	}
}

// laneNameFor finds the ModelPath field that serves one task.
//
// The match is the field name against the task name with the underscores
// removed, which is deliberately looser than the identifier gen-aitasks emits:
// re-spelling that renderer here would make this test agree with a copy of the
// rule rather than with the rule. modellanes_test.go is what pins the exact
// name; this only needs to find the lane.
func laneNameFor(task string) (string, bool) {
	flattened := strings.ReplaceAll(task, "_", "")
	pathType := reflect.TypeOf(compose.ModelPath{})
	for i := range pathType.NumField() {
		field := pathType.Field(i)
		if field.IsExported() && strings.EqualFold(field.Name, flattened) {
			return field.Name, true
		}
	}
	return "", false
}

// lanesWiredByProcessRoles returns the ModelPath field names some process role
// hands to a call. The roles are every package under cmd/, read from the tree
// so a fifth binary is enrolled by existing.
func lanesWiredByProcessRoles(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir("cmd")
	if err != nil {
		t.Fatalf("reading the process roles under cmd/: %v", err)
	}
	wired := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for lane := range lanesWiredIn(t, filepath.Join("cmd", entry.Name())) {
			wired[lane] = true
		}
	}
	return wired
}

// lanesWiredIn reads one role's package: which of its identifiers hold a
// compose.ModelPath, and which fields it selects off them as an argument to a
// call.
//
// "As an argument" is the whole point — `if modelPath.SiteExtract == nil` is a
// read, but it wires nothing, and a lane that satisfied this gate by being
// nil-checked would be as unshipped as one nobody mentions at all.
func lanesWiredIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		// Test files are excluded: this asks what the SHIPPED binary wires,
		// and a lane a role only ever reads from its own test is a lane
		// production never hands to anything.
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", filepath.Join(dir, name), parseErr)
		}
		files = append(files, file)
	}
	holders := modelPathHolders(files)
	wired := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(inner ast.Node) bool {
					if sel, isSel := inner.(*ast.SelectorExpr); isSel {
						if base, isIdent := sel.X.(*ast.Ident); isIdent && holders[base.Name] {
							wired[sel.Sel.Name] = true
						}
					}
					return true
				})
			}
			return true
		})
	}
	return wired
}

// modelPathHolders returns the identifiers in one role's package that hold a
// compose.ModelPath: the parameters and variables declared as one, plus the
// results of the package's own resolver — every role resolves its path through
// a local helper (`modelPath, err := selectModelPath(…)`), so a scan that only
// read explicit types would find the wiring in neither binary.
func modelPathHolders(files []*ast.File) map[string]bool {
	producers := map[string]map[int]bool{} // local func name -> result indexes that are a model path
	for _, file := range files {
		local := composeLocalName(file)
		for _, decl := range topLevelFuncs(file) {
			if decl.Recv != nil || decl.Type.Results == nil {
				continue
			}
			indexes := map[int]bool{}
			for i, result := range flattenFields(decl.Type.Results) {
				if isModelPathType(result, local) {
					indexes[i] = true
				}
			}
			if len(indexes) > 0 {
				producers[decl.Name.Name] = indexes
			}
		}
	}

	holders := map[string]bool{}
	for _, file := range files {
		local := composeLocalName(file)
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				collectTypedNames(typed.Type.Params, local, holders)
				collectTypedNames(typed.Type.Results, local, holders)
			case *ast.ValueSpec:
				if isModelPathType(typed.Type, local) {
					for _, name := range typed.Names {
						holders[name.Name] = true
					}
				}
			case *ast.AssignStmt:
				collectResolvedNames(typed, local, producers, holders)
			}
			return true
		})
	}
	return holders
}

// collectResolvedNames enrolls `x, err := resolve(…)` where resolve is a local
// producer or compose.NewModelPath, and only the result position that is
// actually the path.
func collectResolvedNames(assign *ast.AssignStmt, composeLocal string, producers map[string]map[int]bool, holders map[string]bool) {
	if assign.Tok != token.DEFINE || len(assign.Rhs) != 1 {
		return
	}
	call, isCall := assign.Rhs[0].(*ast.CallExpr)
	if !isCall {
		return
	}
	var indexes map[int]bool
	switch callee := call.Fun.(type) {
	case *ast.Ident:
		indexes = producers[callee.Name]
	case *ast.SelectorExpr:
		if base, isIdent := callee.X.(*ast.Ident); isIdent && base.Name == composeLocal && strings.HasSuffix(callee.Sel.Name, "ModelPath") {
			indexes = map[int]bool{0: true}
		}
	}
	for i, lhs := range assign.Lhs {
		if name, isIdent := lhs.(*ast.Ident); isIdent && indexes[i] {
			holders[name.Name] = true
		}
	}
}

// collectTypedNames enrolls the named parameters or results declared as a
// compose.ModelPath.
func collectTypedNames(list *ast.FieldList, composeLocal string, holders map[string]bool) {
	if list == nil {
		return
	}
	for _, field := range list.List {
		if !isModelPathType(field.Type, composeLocal) {
			continue
		}
		for _, name := range field.Names {
			holders[name.Name] = true
		}
	}
}

// flattenFields expands a field list to one entry per declared name, so a
// result's index is the index a caller destructures.
func flattenFields(list *ast.FieldList) []ast.Expr {
	var out []ast.Expr
	for _, field := range list.List {
		repeat := len(field.Names)
		if repeat == 0 {
			repeat = 1
		}
		for range repeat {
			out = append(out, field.Type)
		}
	}
	return out
}

// isModelPathType reports whether expr names compose.ModelPath, by value or by
// pointer — the two roles differ on which they pass around.
func isModelPathType(expr ast.Expr, composeLocal string) bool {
	if star, isStar := expr.(*ast.StarExpr); isStar {
		expr = star.X
	}
	sel, isSel := expr.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != "ModelPath" {
		return false
	}
	base, isIdent := sel.X.(*ast.Ident)
	return isIdent && base.Name == composeLocal
}

// composeLocalName is the name internal/compose is spelled under in one file:
// its alias, or the package name when the import is plain. A file that does not
// import it yields a name no selector can match.
func composeLocalName(file *ast.File) string {
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if path != composeImportPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "compose"
	}
	return ""
}

// topLevelFuncs returns one file's top-level function declarations.
func topLevelFuncs(file *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			out = append(out, fn)
		}
	}
	return out
}
