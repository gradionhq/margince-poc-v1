// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Structural fitness functions (architecture/03 §1): these tests make the
// boundary rules mechanical, and they derive the package list from the
// tree instead of maintaining it by hand — a new package is enrolled the
// moment it exists (fitness function over point fix). depguard and
// go-arch-lint cover the same rules as lint gates; this covers them as a
// plain `go test` no contributor can skip.

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/gradionhq/margince/backend"

// packagesUnder walks root and returns the import-path-relative directory
// of every Go package beneath it (root itself included when it holds Go
// files). Vendor-less repo: every *.go directory is a package.
func packagesUnder(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var pkgs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if !seen[dir] {
			seen[dir] = true
			pkgs = append(pkgs, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return pkgs
}

// projectImports resolves a package directory and returns its non-stdlib
// imports (tests included — a test-only forbidden edge is still a
// forbidden edge).
func projectImports(t *testing.T, dir string) []string {
	t.Helper()
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			return nil
		}
		t.Fatalf("resolving %s: %v", dir, err)
	}
	var out []string
	for _, group := range [][]string{pkg.Imports, pkg.TestImports, pkg.XTestImports} {
		for _, imp := range group {
			if strings.Contains(imp, ".") {
				out = append(out, imp)
			}
		}
	}
	return out
}

// TestPlatformOwnsNoDomain: internal/platform is technical plumbing
// (ADR-0054 §5) — it may import shared and other platform packages, but
// never a domain module, the composition layer, or a process role.
func TestPlatformOwnsNoDomain(t *testing.T) {
	forbidden := []string{
		modulePath + "/internal/modules/",
		modulePath + "/internal/compose",
		modulePath + "/cmd/",
	}
	for _, dir := range packagesUnder(t, "internal/platform") {
		for _, imp := range projectImports(t, dir) {
			for _, bad := range forbidden {
				if strings.HasPrefix(imp, bad) {
					t.Errorf("%s imports %s: platform owns no domain", dir, imp)
				}
			}
		}
	}
}

// TestNoSiblingModuleImports: a module reaches another module's behavior
// only through a shared/ports seam or a dependency injected by the
// composition layer (ADR-0054 §9) — never by importing the sibling. The
// module list is derived from the tree, so a new module is enrolled the
// moment its directory exists.
func TestNoSiblingModuleImports(t *testing.T) {
	modules, err := filepath.Glob("internal/modules/*")
	if err != nil {
		t.Fatal(err)
	}
	for _, mod := range modules {
		modImportPrefix := modulePath + "/" + filepath.ToSlash(mod)
		for _, dir := range packagesUnder(t, mod) {
			for _, imp := range projectImports(t, dir) {
				if !strings.HasPrefix(imp, modulePath+"/internal/modules/") {
					continue
				}
				if imp == modImportPrefix || strings.HasPrefix(imp, modImportPrefix+"/") {
					continue // a module may import its own subpackages
				}
				t.Errorf("%s imports %s: modules never import a sibling module", dir, imp)
			}
		}
	}
}

// TestSharedIsPure: internal/shared (kernel + apperrors + ports) is the
// Tier-0 leaf layer — stdlib and each other, nothing else. A third-party
// or project import here is an architecture defect.
func TestSharedIsPure(t *testing.T) {
	for _, dir := range packagesUnder(t, "internal/shared") {
		for _, imp := range projectImports(t, dir) {
			if strings.HasPrefix(imp, modulePath+"/internal/shared/") {
				continue // leaf → leaf is allowed (types only, no cycles)
			}
			t.Errorf("%s imports %s: shared packages must be stdlib-only", dir, imp)
		}
	}
}

// modelClientConstructors is every call that mints a model client or a
// router in front of one. internal/modules/ai builds these because that
// IS the one gate, and the two sanctioned assemblies in internal/compose
// (brain.go's NewModelPath/NewLocalModelPath, sitereaddebug.go's DB-less
// debug wiring) are the only other callers. Anything else is a second
// gate and must be rejected on sight.
var modelClientConstructors = []string{"ai.NewFakeClient(", "ai.NewRouter(", "ai.NewLocalRouter("}

// modelPathAssemblySeam: the only non-test, non-ai-module files allowed to
// call a modelClientConstructor. This is enumerated, not tree-derived,
// because the invariant it encodes — "these two files ARE the seam" — is
// a statement about what the files DO, not where they sit; nothing about
// their location distinguishes them from any other file in
// internal/compose. Growing this list is itself the review signal: a
// third entry is a second gate and should be rejected on sight, not waved
// through because the rule technically allows appending to it.
var modelPathAssemblySeam = map[string]bool{
	"internal/compose/brain.go":         true,
	"internal/compose/sitereaddebug.go": true,
}

// TestNoModelClientOutsideTheGate: walks every hand-written, non-test Go
// file and fails if it constructs a model client outside the seam above,
// or if a cmd/ process role calls compose.NewLocalModelPath — that
// assembly is DB-less by design (siteread debug, tests); a production
// role has a pool and belongs on the metered compose.NewModelPath.
func TestNoModelClientOutsideTheGate(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path)
		if strings.HasPrefix(rel, "internal/modules/ai/") {
			return nil // the gate's own module: constructing clients here is its job
		}
		src, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.go file from walking the trusted source tree
		if err != nil {
			return err
		}
		text := string(src)

		if !modelPathAssemblySeam[rel] {
			for _, ctor := range modelClientConstructors {
				if strings.Contains(text, ctor) {
					t.Errorf("%s: calls %s outside the compose gate — only internal/compose/brain.go and internal/compose/sitereaddebug.go may construct a model client directly", rel, strings.TrimSuffix(ctor, "("))
				}
			}
		}

		if strings.HasPrefix(rel, "cmd/") && strings.Contains(text, "compose.NewLocalModelPath(") {
			t.Errorf("%s: a cmd/ process role calls compose.NewLocalModelPath — that seam is DB-less by design; production roles resolve through the metered compose.NewModelPath", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking backend tree: %v", err)
	}
}

// funcsCallingSelector returns, for every top-level function declared in
// the non-test Go files directly under dir, the "file:func" label of each
// function whose body contains a call shaped like pkgIdent.funcName
// (e.g. "compose.NewModelPath"). It counts distinct FUNCTIONS, not raw
// call-expression occurrences: a function is allowed to branch to more
// than one config (modelwiring.go's resolveModelPath calls
// compose.NewModelPath from both its routing-file and --ai-fake arms) and
// still be the package's single decision point — what the fitness rule
// below guards against is a SECOND function growing its own competing
// resolution.
func funcsCallingSelector(t *testing.T, dir, pkgIdent, funcName string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var sites []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if ok && ident.Name == pkgIdent && sel.Sel.Name == funcName {
					calls = true
				}
				return true
			})
			if calls {
				sites = append(sites, filepath.ToSlash(path)+":"+fn.Name.Name)
			}
		}
	}
	return sites
}

// TestOneModelPathPerRole: each cmd/<role> process resolves its AI
// surfaces through exactly one function that calls compose.NewModelPath —
// the single decision modelwiring.go's resolveModelPath documents ("a
// process holds one Router, one cache, one budget — never a doubled pair
// from two callers each resolving their own"). A second function calling
// it would be a second, competing resolution point the role never
// intended to grow. The role list is derived from the tree, so a new
// cmd/<role> is enrolled the moment its directory exists.
func TestOneModelPathPerRole(t *testing.T) {
	roles, err := filepath.Glob("cmd/*")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		info, err := os.Stat(role)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			continue
		}
		sites := funcsCallingSelector(t, role, "compose", "NewModelPath")
		if len(sites) > 1 {
			t.Errorf("%s: %d functions call compose.NewModelPath (%s) — a role resolves its model path in exactly one place", role, len(sites), strings.Join(sites, ", "))
		}
	}
}
