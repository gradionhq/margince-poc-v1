// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Extension-tier fitness functions (ADR-0069 §3): the compiler already
// walls extensions off from internal/** (their module paths sit outside
// the backend module), these tests hold the rest of the import contract
// from the tree — every extension source dir (enabled or fixture) is
// enrolled the moment it exists.

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const compositionModulePath = "github.com/gradionhq/margince/composition"

// extensionTrees lists every extension source directory: the enabled set
// under ../extensions plus the CI fixtures under ../fixtures/extensions.
func extensionTrees(t *testing.T) map[string]string {
	t.Helper()
	trees := map[string]string{}
	for _, root := range []string{"../extensions", "../fixtures/extensions"} {
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				trees[filepath.ToSlash(filepath.Join(root, e.Name()))] = e.Name()
			}
		}
	}
	return trees
}

// extensionModulePaths reads each extension tree's go.mod module path —
// the deny-set for sibling imports and for the core side below.
func extensionModulePaths(t *testing.T, trees map[string]string) map[string]string {
	t.Helper()
	paths := map[string]string{}
	for dir := range trees {
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if os.IsNotExist(err) {
			continue // frontend-only unit: no Go module to constrain
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				paths[dir] = strings.TrimSpace(rest)
				break
			}
		}
		if paths[dir] == "" {
			t.Fatalf("%s/go.mod declares no module path", dir)
		}
	}
	return paths
}

// goImports parses every .go file under dir (tests included) and returns
// file → imports, without compiling anything. Paths are decoded with
// strconv.Unquote: the compiler accepts a raw-string (backtick) import
// path too, and naive quote-trimming would let exactly that form slide
// past every deny rule below.
func goImports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			decoded, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: import path %s does not decode as a Go string literal: %w", path, imp.Path.Value, err)
			}
			out[filepath.ToSlash(path)] = append(out[filepath.ToSlash(path)], decoded)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// extensionSurfaceMarker is the allowlist directive (ADR-0069 §3):
// membership in backend/pkg alone grants nothing — a package is
// extension surface only when its package clause carries this line.
const extensionSurfaceMarker = "//margince:extension-surface"

// allowlistedSurface derives the published allowlist from the tree: the
// import path of every backend/pkg package whose non-test source carries
// the marker directive.
func allowlistedSurface(t *testing.T) map[string]bool {
	t.Helper()
	marked := map[string]bool{}
	err := filepath.WalkDir("pkg", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path) // #nosec G304 -- path from walking the trusted source tree
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			if strings.TrimSpace(line) == extensionSurfaceMarker {
				marked[modulePath+"/"+filepath.ToSlash(filepath.Dir(path))] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return marked
}

// TestExtensionsImportOnlyTheAllowlistedSurface: an extension reaches the
// product only through the marker-allowlisted backend/pkg packages —
// never internal/**, cmd/**, an unmarked pkg package, the composition
// module, or a sibling extension (EXT-P2/P7). Third-party imports are
// the extension's own business (its go.mod carries them).
func TestExtensionsImportOnlyTheAllowlistedSurface(t *testing.T) {
	trees := extensionTrees(t)
	modPaths := extensionModulePaths(t, trees)
	surface := allowlistedSurface(t)
	for dir := range trees {
		for file, imports := range goImports(t, dir) {
			for _, imp := range imports {
				if strings.HasPrefix(imp, modulePath+"/") {
					if !surface[imp] {
						t.Errorf("%s imports %s: extensions import only the allowlisted published surface (a backend/pkg package carrying %s)", file, imp, extensionSurfaceMarker)
					}
					continue
				}
				if imp == compositionModulePath || strings.HasPrefix(imp, compositionModulePath+"/") {
					t.Errorf("%s imports %s: the composed wiring is core output, never extension input", file, imp)
				}
				for sibDir, sibMod := range modPaths {
					if sibDir == dir {
						continue
					}
					if imp == sibMod || strings.HasPrefix(imp, sibMod+"/") {
						t.Errorf("%s imports %s: extensions never import a sibling extension", file, imp)
					}
				}
			}
		}
	}
}

// TestSurfaceMarkerLivesOnlyUnderPkg: the directive grants surface
// membership, so a stray copy outside backend/pkg would be a silent
// allowlist widening — the marker is meaningless (and refused) anywhere
// else in the backend tree. Test files are exempt: the allowlist
// derivation never reads them (this file's own constant included).
func TestSurfaceMarkerLivesOnlyUnderPkg(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel := filepath.ToSlash(path)
		if strings.HasPrefix(rel, "pkg/") {
			return nil
		}
		src, err := os.ReadFile(path) // #nosec G304 -- path from walking the trusted source tree
		if err != nil {
			return err
		}
		if strings.Contains(string(src), extensionSurfaceMarker) {
			t.Errorf("%s carries %s: the surface marker belongs only on backend/pkg packages", rel, extensionSurfaceMarker)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCompositionWiredOnlyFromCmd: the backend imports the composed
// extension set exactly at the four role mains — anywhere else would be
// a second composition path — and never imports an extension module
// directly (extensions reach the backend only through the generated
// compose file, ADR-0069 §3).
func TestCompositionWiredOnlyFromCmd(t *testing.T) {
	extMods := extensionModulePaths(t, extensionTrees(t))
	for file, imports := range goImports(t, ".") {
		if strings.HasPrefix(file, "tools/") {
			continue // the generator authors these strings; it wires nothing
		}
		for _, imp := range imports {
			if imp == compositionModulePath && !strings.HasPrefix(file, "cmd/") {
				t.Errorf("%s imports the composition module: only cmd/<role> mains wire the composed extension set", file)
			}
			for _, mod := range extMods {
				if imp == mod || strings.HasPrefix(imp, mod+"/") {
					t.Errorf("%s imports extension module %s: the backend reaches extensions only through the generated composition", file, mod)
				}
			}
		}
	}
}
