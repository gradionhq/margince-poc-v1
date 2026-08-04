// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A registry that claims to be complete must be.
//
// The governed tool surface is wired by a family of package-level registrars,
// and three builders each claim to invoke EVERY one of them: production's, and
// two test registries that exist only to walk the whole surface — one proving
// each tool reaches its seam, one proving the whole tool list encodes. A
// registrar missing from one of them shrinks that walk in silence: the walk
// still passes, over a smaller set than it names.
//
// The three are deliberately not collapsed into one builder. The tests are
// package agents and production is package compose, and their seams differ on
// purpose — one wires seams that return a sentinel, to prove the seam was
// reached; one wires inert seams, to prove encoding; production wires the real
// ones. So the registrar list is DERIVED from the package and the parity
// asserted, rather than hand-kept in three places and hoped equal.
//
// The derivation keys on the parameter's TYPE, not on the function's name
// shape: a registrar may wire one tool or a family, and the parameter it takes
// may be called anything.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

const (
	agentsDir        = "internal/modules/agents"
	agentsImportPath = "github.com/gradionhq/margince/backend/" + agentsDir

	// registryType is the type a tool registrar takes: taking it is what makes
	// a function a registrar.
	registryType = "Registry"

	// registrarPrefix narrows the candidates the parameter type then decides.
	registrarPrefix = "Register"

	// toolRegistrarFloor is the vacuity floor: a derivation that discovered
	// nothing would certify all three builders at once.
	toolRegistrarFloor = 7
)

// fullToolRegistries are the builders that each wire the whole tool surface,
// named by the function that does the wiring, with what its completeness buys.
var fullToolRegistries = []struct{ path, builder, claim string }{
	{
		"internal/compose/registry.go", "registryWithGate",
		"the surface a client is served: an unregistered tool does not exist",
	},
	{
		agentsDir + "/conformance_test.go", "fullRegistry",
		"the encoding walk: an unregistered tool's schema is never checked",
	},
	{
		agentsDir + "/idargs_test.go", "idProbeDispatcher",
		"the seam walk: an unregistered tool's arguments are never dispatched",
	},
}

// toolRegistrar is one discovered registrar and where it is declared.
type toolRegistrar struct{ name, at string }

func TestEveryToolRegistrarIsInvokedByEveryFullRegistry(t *testing.T) {
	registrars, methods := toolRegistrars(t)
	if len(registrars) < toolRegistrarFloor {
		t.Fatalf("discovered only %d tool registrars in %s, expected at least %d: the AST derivation broke, not the subject — a registrar is a package-level func taking *%s, and this gate is currently certifying every builder against an empty list",
			len(registrars), agentsDir, toolRegistrarFloor, registryType)
	}

	// The door every tool comes through is a METHOD on the registry, not a
	// registrar of tools. Counting it would inflate the discovered list by one
	// name no builder ever calls, and the parity assertion would fail for a
	// reason that has nothing to do with any tool.
	if len(methods) == 0 {
		t.Fatalf("no %s* method on *%s found in %s: the receiver-based exclusion below has nothing to exclude, so it no longer proves anything",
			registrarPrefix, registryType, agentsDir)
	}
	for _, method := range methods {
		for _, r := range registrars {
			if r.name == method {
				t.Errorf("%s is a method on *%s and was discovered as a registrar (%s): a receiver makes it the registry's own door, not a tool family, and no builder calls it",
					method, registryType, r.at)
			}
		}
	}

	for _, registry := range fullToolRegistries {
		called := registrarCalls(t, registry.path, registry.builder)
		for _, r := range registrars {
			if called[r.name] {
				continue
			}
			t.Errorf("%s (%s) does not invoke %s, declared at %s — %s. Wire it there, or move it off *%s if it is not a tool registrar",
				registry.builder, registry.path, r.name, r.at, registry.claim, registryType)
		}
	}
}

// toolRegistrars discovers the package-level tool registrars of the agents
// package, and separately the Register* methods on the registry type — the
// receiver is what tells the two apart.
func toolRegistrars(t *testing.T) (registrars []toolRegistrar, methods []string) {
	t.Helper()
	fset := token.NewFileSet()
	files := parsePackageDir(t, fset, agentsDir) // reused from versionguard_test.go
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, registrarPrefix) {
				continue
			}
			if fn.Recv != nil {
				if len(fn.Recv.List) == 1 && pointsAtRegistry(fn.Recv.List[0].Type) {
					methods = append(methods, fn.Name.Name)
				}
				continue
			}
			params := fn.Type.Params
			if params == nil || len(params.List) == 0 || !pointsAtRegistry(params.List[0].Type) {
				continue
			}
			registrars = append(registrars, toolRegistrar{fn.Name.Name, fset.Position(fn.Pos()).String()})
		}
	}
	sort.Slice(registrars, func(i, j int) bool { return registrars[i].name < registrars[j].name })
	sort.Strings(methods)
	return registrars, methods
}

// pointsAtRegistry reports whether an expression spells the type *Registry.
func pointsAtRegistry(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	name, ok := star.X.(*ast.Ident)
	return ok && name.Name == registryType
}

// registrarCalls returns the function names one builder calls, whether as a
// bare identifier (the builders inside package agents) or through the agents
// import (the production builder in package compose).
func registrarCalls(t *testing.T, path, builder string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	local := localImportName(file, agentsImportPath)

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == builder {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s: builder %s not found — the parity derivation lost one of the registries it compares, so it can no longer hold that registry to the list", path, builder)
	}

	called := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			called[fun.Name] = true
		case *ast.SelectorExpr:
			if pkg, ok := fun.X.(*ast.Ident); ok && local != "" && pkg.Name == local {
				called[fun.Sel.Name] = true
			}
		}
		return true
	})
	return called
}

// localImportName gives the name an import path is spelled as in one file —
// its alias when it carries one, otherwise the path's last segment. Empty when
// the file does not import it at all, which is the case for the two builders
// that live in the package itself.
func localImportName(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"`+path+`"` {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}
