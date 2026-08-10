// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integration

// The AC-OV-1 half of the AC-OV acceptance suite (overlay_acceptance_test.go
// carries the suite's own scope doc): the seam is proven by the SHAPE of the
// tree's import graph rather than by any runtime call, so it needs no database
// and shares no fixture with the behavioural criteria.
//
// Untagged for that reason. It is the one file in this package that asserts
// nothing a database could answer, and a criterion provable in milliseconds
// should not wait on `make db-up` to be checked — a gate that runs only in the
// slow lane runs less often than the thing it guards changes.

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backendModulePath is this module's own import path — spelled once so
// every import-path comparison below reads the same literal arch_test.go
// (backend/arch_test.go) already pins at the repo root.
const backendModulePath = "github.com/gradionhq/margince/backend"

// backendModuleRoot resolves the backend Go module's root directory from
// this test file's own location: `go test` always chdirs into the
// package directory it is testing (here,
// backend/internal/compose/integration), so three levels up is the
// module root — verified against go.mod rather than assumed, so a future
// package move fails loudly instead of silently walking the wrong tree.
func backendModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolving the backend module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved %q as the backend module root, but it has no go.mod: %v", root, err)
	}
	return root
}

// acceptancePackagesUnder/acceptanceDirectImports are the same
// tree-derived, direct-import-only technique backend/arch_test.go's own
// packagesUnder/projectImports use (that file lives in package
// backendarch at the module root, which holds no importable production
// code, so this suite — living in package integration — carries its own
// copy rather than reach across an import boundary that does not exist).
func acceptancePackagesUnder(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
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
			dirs = append(dirs, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return dirs
}

// acceptanceImportContext is build.Default with the "integration" build tag
// appended: the zero-value build.Context (build.ImportDir's default) does
// NOT satisfy `//go:build integration`, so every integration-tagged file —
// including this very suite's own package and sibling packages like
// internal/modules/agents that carry integration test files — would be
// dropped into Context.ImportDir's IgnoredGoFiles and never scanned for
// imports at all. That would make an incumbent import smuggled into any
// integration-tagged file above the seam invisible to this gate. Starting
// from build.Default (not the zero value) also keeps GOOS/GOARCH/GOPATH
// correct for the host running the test.
func acceptanceImportContext() build.Context {
	ctx := build.Default
	ctx.BuildTags = append(append([]string{}, ctx.BuildTags...), "integration")
	return ctx
}

func acceptanceDirectImports(t *testing.T, dir string) []string {
	t.Helper()
	ctx := acceptanceImportContext()
	pkg, err := ctx.ImportDir(dir, 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			// A directory that genuinely holds no Go source files at all
			// (not even integration-tagged ones) has nothing to scan —
			// distinct from any other resolution error, which must
			// surface rather than be swallowed as "no imports" (T2).
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

// TestAcceptance_AC_OV_1_NoIncumbentImportAboveSeam proves design.md's
// AC-OV-1 (subsystems/overlay-augmentation.md: "the three AI layers and
// UI call only the SoR Provider interface — no direct incumbent-API or
// direct crm-core call exists above the seam"): no package outside
// internal/modules/overlay's own tree imports overlay/hubspot directly.
//
// internal/compose (the composition ROOT package only, ADR-0054/A69) is
// the one sanctioned exception — it is where the Dispatcher/Provider seam
// is WIRED to the concrete hubspot.Adapter (compose/overlay.go,
// compose/jobs.go), which is BELOW/AT the seam, not above it. Every
// compose SUBPACKAGE (this package included) gets no such exception: this
// test itself proves, by construction, that reuse-driving-the-fake
// throughout this suite never had to reach for hubspot directly either.
func TestAcceptance_AC_OV_1_NoIncumbentImportAboveSeam(t *testing.T) {
	root := backendModuleRoot(t)
	hubspotImportPath := backendModulePath + "/internal/modules/overlay/hubspot"
	overlayModulePrefix := backendModulePath + "/internal/modules/overlay"
	composeRootImportPath := backendModulePath + "/internal/compose"

	dirToImportPath := func(dir string) string {
		rel := strings.TrimPrefix(dir, filepath.ToSlash(root)+"/")
		return backendModulePath + "/" + rel
	}

	for _, sub := range []string{"internal/modules", "internal/platform", "internal/compose", "internal/contracts", "cmd"} {
		for _, dir := range acceptancePackagesUnder(t, filepath.Join(root, filepath.FromSlash(sub))) {
			importPath := dirToImportPath(dir)
			if strings.HasPrefix(importPath, overlayModulePrefix) {
				continue // the seam's own inside — overlay may reference its own hubspot subpackage's siblings (e.g. shared test fixtures)
			}
			if importPath == composeRootImportPath {
				continue // the ONE sanctioned composition root (see doc above)
			}
			for _, imp := range acceptanceDirectImports(t, dir) {
				if imp == hubspotImportPath {
					t.Errorf("%s imports %s directly — no package above the seam may import the incumbent adapter (AC-OV-1)", importPath, imp)
				}
			}
		}
	}

	// Positive half: the modules that DO reach records above the overlay
	// seam (the governed agent tool surface, the inbound capture sink,
	// and search's retrieval join) reach them ONLY through
	// ports/datasource — never overlay directly, confirming they are
	// exactly the "layers above the seam" AC-OV-1 describes and that the
	// seam is actually load-bearing for them, not merely unused.
	datasourcePath := backendModulePath + "/internal/shared/ports/datasource"
	for _, mod := range []string{"agents", "capture", "search"} {
		modDir := filepath.Join(root, "internal", "modules", mod)
		sawDatasource := false
		for _, dir := range acceptancePackagesUnder(t, modDir) {
			for _, imp := range acceptanceDirectImports(t, dir) {
				if imp == datasourcePath {
					sawDatasource = true
				}
				if strings.HasPrefix(imp, overlayModulePrefix) {
					t.Errorf("%s imports %s — the %s module must reach records only through %s, never overlay directly", dirToImportPath(dir), imp, mod, datasourcePath)
				}
			}
		}
		if !sawDatasource {
			t.Errorf("module %q never imports %s — expected it to reach records through the frozen SoR Provider seam", mod, datasourcePath)
		}
	}
}
