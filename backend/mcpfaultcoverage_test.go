// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A module's typed refusal must be legible on EVERY surface that can reach it,
// not just the one it was written for.
//
// The trap this gate closes: a module maps its own typed errors onto 422 wire
// shapes inside an HTTP helper (`writeStoreErr` and its siblings). That helper
// runs for REST and for nothing else. The MCP tool surface reaches the SAME
// stores through the datasource seam, so it never runs that helper — and an
// error nothing classifies reaches the agent as "the tool failed for an
// internal reason; retry", which is both false and unactionable: it withholds
// the field the agent could have fixed and sends it to retry a call the server
// has already settled. That is how a missing timezone offset and a missing
// display_name both became "contact your workspace admin".
//
// The invariant, and it is derived from the tree rather than listed here: for a
// module that exposes a datasource provider — the MCP surface's door — every
// typed error its transport maps to httperr.Validation must ALSO carry that
// verdict on the error itself, by implementing apperrors.FieldFault. Then
// httperr.Classify answers it on both surfaces from one mapping, and there is
// no second copy to fall behind.
//
// Modules with no provider are out of scope on purpose: MCP cannot reach their
// stores, so a transport-owned mapping there is complete. A module that GAINS a
// provider inherits the obligation automatically, which is the point.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulesDir = "internal/modules"

// seamImportPath is the datasource seam — the MCP surface's door into a
// module's stores. Importing it is the marker, deliberately BROADER than
// "implements SystemOfRecordProvider": that interface is satisfied
// structurally, so the three modules that actually serve it name it in a
// comment and nowhere else, and a gate keyed on the name skipped exactly them.
// A gate that over-obligates costs one method on a type that did not strictly
// need it; a gate that under-obligates reads green while the bug ships.
const seamImportPath = "github.com/gradionhq/margince/backend/internal/shared/ports/datasource"

// moduleSource is one module's parsed non-test files.
type moduleSource struct {
	name  string
	files map[string]*ast.File
}

func parseModules(t *testing.T) []moduleSource {
	t.Helper()
	byModule := map[string]map[string]*ast.File{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(modulesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		path = filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		rest := strings.TrimPrefix(path, modulesDir+"/")
		module := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			module = rest[:i]
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		if byModule[module] == nil {
			byModule[module] = map[string]*ast.File{}
		}
		byModule[module][path] = file
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", modulesDir, err)
	}
	if len(byModule) == 0 {
		t.Fatalf("no modules found under %s — the gate is reading the wrong tree", modulesDir)
	}
	out := make([]moduleSource, 0, len(byModule))
	for name, files := range byModule {
		out = append(out, moduleSource{name: name, files: files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// touchesDatasourceSeam reports whether the module imports the seam, i.e.
// whether an MCP tool call can reach its code at all.
func (m moduleSource) touchesDatasourceSeam() bool {
	for _, file := range m.files {
		for _, imp := range file.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == seamImportPath {
				return true
			}
		}
	}
	return false
}

// faultMethods are the three shapes a module error may use to carry its own
// verdict: one field, several fields, or a condition that names no field at all.
// Any of them satisfies the obligation, because any of them makes the refusal
// classify on every surface.
var faultMethods = map[string]bool{"FieldFault": true, "FieldFaults": true, "MessageFault": true}

// implementsFieldFault collects the error types in this module that carry
// their own verdict, by any of the three interface forms.
func (m moduleSource) implementsFieldFault() map[string]bool {
	out := map[string]bool{}
	for _, file := range m.files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if !faultMethods[fn.Name.Name] {
				continue
			}
			if name := receiverTypeName(fn); name != "" {
				out[name] = true
			}
		}
	}
	return out
}

// validationMappedTypes finds the types this module's transport maps onto a
// 422 by hand: an `errors.As(err, &x)` test whose branch calls
// httperr.Validation, where x was declared as a pointer to a module type.
// helpersReturning422 names this module's own functions that BUILD a 422
// DetailedError and return it — customfields.structuralChangeRefused is one.
// A branch calling such a helper maps its error to a 422 just as surely as one
// spelling the literal inline, and a detector that reads only the inline forms
// leaves the same false-clean this gate exists to remove, one indirection away.
func (m moduleSource) helpersReturning422() map[string]bool {
	out := map[string]bool{}
	for _, file := range m.files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			if blockBuilds422(fn.Body) {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

// blockBuilds422 reports whether the block contains a 422 DetailedError literal.
func blockBuilds422(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isHTTPErrDetailedError(lit.Type) {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Status" && isUnprocessableEntity(kv.Value) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (m moduleSource) validationMappedTypes(helpers map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, file := range m.files {
		// Local var name → declared type name, for the `var x *T` decls that
		// errors.As targets.
		declared := map[string]string{}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 {
				return true
			}
			if name, ok := pointerTypeName(spec.Type); ok {
				declared[spec.Names[0].Name] = name
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			target, ok := errorsAsTarget(ifStmt.Cond)
			if !ok {
				return true
			}
			typeName, ok := declared[target]
			if !ok || !mapsTo422(ifStmt.Body, helpers) {
				return true
			}
			out[typeName] = true
			return true
		})
	}
	return out
}

// pointerTypeName reports the name behind a `*T` type expression, for a T
// declared in this package (a qualified `*pkg.T` is another package's error
// and another package's obligation).
func pointerTypeName(expr ast.Expr) (string, bool) {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// errorsAsTarget reports the variable name an `errors.As(err, &x)` call binds.
func errorsAsTarget(cond ast.Expr) (string, bool) {
	call, ok := cond.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "As" {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "errors" {
		return "", false
	}
	unary, ok := call.Args[1].(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return "", false
	}
	ident, ok := unary.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// callsHTTPErrValidation reports whether the block hands a 422 to the wire, by
// EITHER idiom this codebase uses.
//
// Detecting only httperr.Validation was this gate's own false-clean: a module
// may also build the same 422 as an httperr.DetailedError with
// StatusUnprocessableEntity — which is exactly how customfields mapped its
// multi-field ValidationError — and a detector blind to that reported green on
// the very leak it was written to catch. A gate that recognizes one spelling of
// a pattern is a gate that certifies the other spelling.
func mapsTo422(body *ast.BlockStmt, helpers map[string]bool) bool {
	if callsHelper(body, helpers) {
		return true
	}
	return callsHTTPErrValidation(body)
}

// callsHelper reports whether the block calls one of this module's own
// 422-building helpers.
func callsHelper(body *ast.BlockStmt, helpers map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && helpers[fn.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

func callsHTTPErrValidation(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Validation" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "httperr" {
				found = true
				return false
			}
		case *ast.CompositeLit:
			if !isHTTPErrDetailedError(node.Type) {
				return true
			}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Status" {
					continue
				}
				if isUnprocessableEntity(kv.Value) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isHTTPErrDetailedError reports whether expr names httperr.DetailedError.
func isHTTPErrDetailedError(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "DetailedError" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "httperr"
}

// isUnprocessableEntity reports whether expr is http.StatusUnprocessableEntity.
func isUnprocessableEntity(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StatusUnprocessableEntity" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http"
}

func TestSeamReachableModulesCarryTheirOwnFieldVerdict(t *testing.T) {
	obligated := 0
	for _, module := range parseModules(t) {
		if !module.touchesDatasourceSeam() {
			continue
		}
		obligated++
		carries := module.implementsFieldFault()
		mapped := module.validationMappedTypes(module.helpersReturning422())

		names := make([]string, 0, len(mapped))
		for name := range mapped {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if carries[name] {
				continue
			}
			t.Errorf("%s: %s.%s is mapped to a 422 in this module's HTTP transport but does not implement "+
				"apperrors.FieldFault — the MCP surface reaches this module through the datasource seam, never "+
				"through that transport, so it would report this refusal as an internal server fault and tell the "+
				"agent to retry it. Add FieldFault() to the type and delete the transport branch.",
				modulesDir+"/"+module.name, module.name, name)
		}
	}
	if obligated == 0 {
		t.Fatalf("no module imports %s — the marker is stale and this gate is asserting nothing", seamImportPath)
	}
}
