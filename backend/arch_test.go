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
	"go/build"
	"io/fs"
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
