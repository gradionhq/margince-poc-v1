// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVanillaOutputMatchesTheCommittedStub is the two-lane bind: what a
// bare go build wires (the committed composition/ stub) and what a
// composed vanilla build wires (this generator's empty output) must be
// the same bytes. The generator and `-verify` enforce it at gen time;
// this holds it in the unit lane too, where a stub edit fails fastest.
func TestVanillaOutputMatchesTheCommittedStub(t *testing.T) {
	stub, err := os.ReadFile(filepath.Join("..", "..", "..", "composition", "extensions_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stub, extensionsGen(nil, nil, nil)) {
		t.Fatalf("composition/extensions_gen.go differs from the generator's vanilla output:\n--- stub ---\n%s\n--- generated ---\n%s", stub, extensionsGen(nil, nil, nil))
	}
}

func TestExtensionsGenWiresUnitsInSortedOrder(t *testing.T) {
	got := string(extensionsGen([]extensionUnit{
		{Name: "alpha", ModulePath: "example.test/ext/alpha"},
		{Name: "beta", ModulePath: "example.test/ext/beta"},
	}, nil, nil))
	for _, want := range []string{
		"ext0 \"example.test/ext/alpha\"",
		"ext1 \"example.test/ext/beta\"",
		"mustBe(\"alpha\", ext0.New()),\n\t\tmustBe(\"beta\", ext1.New()),",
		"func mustBe(dir string, e extension.Extension) extension.Extension {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated wiring misses %q:\n%s", want, got)
		}
	}
}

// A unit's module path is its own to choose, so the alias order (the enabled
// set's unit-name order, which Extensions() wires in) and gofmt's import order
// (by path) are two different orders. They coincide only while every unit is
// published under the repo's own module prefix — which is exactly the tree the
// generator sees locally, and exactly not the tree the extension-reference CI
// job composes: it copies fixtures/extensions/crm-hello, published at
// example.margince.dev, into the enabled set, where it sorts ahead of every
// github.com/… unit. Emitting the import lines in alias order made that block
// non-canonical and canonicalGoSource refused it.
func TestExtensionsGenSortsImportsByPathNotByAlias(t *testing.T) {
	// alpha is the first unit by name and therefore ext0, but its module path
	// sorts LAST — the two orders disagree in both directions.
	units := []extensionUnit{
		{Name: "alpha", ModulePath: "zebra.test/ext/alpha"},
		{Name: "beta", ModulePath: "aardvark.test/ext/beta"},
	}
	got := string(extensionsGen(units, nil, nil))
	if _, err := canonicalGoSource("extensions_gen.go", []byte(got)); err != nil {
		t.Fatalf("the emitted wiring is not canonical gofmt: %v\n%s", err, got)
	}
	// The import block is path-ordered…
	wantImports := "\text1 \"aardvark.test/ext/beta\"\n\text0 \"zebra.test/ext/alpha\"\n"
	if !strings.Contains(got, wantImports) {
		t.Errorf("imports are not in path order:\n%s", got)
	}
	// …and the wiring is still in unit-name order, with each alias on its own
	// unit. A fix that renumbered the aliases instead would pass the gofmt
	// assertion above and silently reorder the composed set.
	if !strings.Contains(got, "mustBe(\"alpha\", ext0.New()),\n\t\tmustBe(\"beta\", ext1.New()),") {
		t.Errorf("the wiring order or the alias binding changed:\n%s", got)
	}
}

// TestEmittedWiringIsCanonicalGoSource: the emitter must produce parsing,
// gofmt-canonical bytes itself — canonicalGoSource is the gen-time gate
// that turns a template bug into a named error instead of a failure at
// the next go build (and a formatting drift into an error instead of a
// silent byte-identity break).
func TestEmittedWiringIsCanonicalGoSource(t *testing.T) {
	for name, units := range map[string][]extensionUnit{
		"vanilla": nil,
		"composed": {
			{Name: "alpha", ModulePath: "example.test/ext/alpha"},
			{Name: "beta", ModulePath: "example.test/ext/beta"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := canonicalGoSource("extensions_gen.go", extensionsGen(units, nil, nil)); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("a parse error is a gen-time error", func(t *testing.T) {
		if _, err := canonicalGoSource("broken.go", []byte("package x\nfunc {")); err == nil || !strings.Contains(err.Error(), "does not parse") {
			t.Fatalf("err = %v, want the parse rejection", err)
		}
	})

	t.Run("non-canonical formatting is an error, never adopted", func(t *testing.T) {
		if _, err := canonicalGoSource("ugly.go", []byte("package x\nvar  a  =  1\n")); err == nil || !strings.Contains(err.Error(), "not canonical gofmt") {
			t.Fatalf("err = %v, want the formatting rejection", err)
		}
	})
}

// writeUnit lays out one extension dir under a temp extensions/ root.
func writeUnit(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "extensions", name)
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanExtensions(t *testing.T) {
	goMod := "module example.test/ext/a\n\ngo 1.26.5\n"
	cases := []struct {
		name    string
		unit    string
		files   map[string]string
		wantErr string
	}{
		{name: "go files without a module", unit: "no-mod", files: map[string]string{"a.go": "package a\n"}, wantErr: "no go.mod"},
		{name: "module without a root package", unit: "no-pkg", files: map[string]string{"go.mod": goMod}, wantErr: "no root package"},
		{name: "invalid unit name", unit: "Bad_Name", files: map[string]string{}, wantErr: "not a valid unit name"},
		// api/ used to be here; it has a composition now (contracts.go), and
		// TestApiLayerIsGovernedByItsOwnRule plus TestFragmentRefusals pin
		// what replaced the blanket refusal.
		{name: "unbuilt capability layer", unit: "with-frontend", files: map[string]string{"go.mod": goMod, "a.go": "package a\n", "frontend/app.tsx": "export {};\n"}, wantErr: "frontend/ composition is not built yet"},
		{name: "empty unit", unit: "empty", files: map[string]string{}, wantErr: "nothing to compose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeUnit(t, root, tc.unit, tc.files)
			_, err := scanExtensions(root)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	t.Run("well-formed unit", func(t *testing.T) {
		root := t.TempDir()
		writeUnit(t, root, "b-unit", map[string]string{"go.mod": "module example.test/ext/b\n\ngo 1.26.5\n", "b.go": "package b\n"})
		writeUnit(t, root, "a-unit", map[string]string{"go.mod": goMod, "a.go": "package a\n"})
		units, err := scanExtensions(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(units) != 2 || units[0].Name != "a-unit" || units[1].Name != "b-unit" {
			t.Fatalf("units = %+v, want a-unit before b-unit", units)
		}
		if units[0].ModulePath != "example.test/ext/a" {
			t.Fatalf("module path = %q", units[0].ModulePath)
		}
	})

	t.Run("missing extensions dir is vanilla", func(t *testing.T) {
		units, err := scanExtensions(t.TempDir())
		if err != nil || units != nil {
			t.Fatalf("units, err = %v, %v — want the empty set", units, err)
		}
	})

	t.Run("symlinked unit is refused, not skipped", func(t *testing.T) {
		root := t.TempDir()
		writeUnit(t, root, "real", map[string]string{"go.mod": goMod, "a.go": "package a\n"})
		if err := os.Symlink(filepath.Join(root, "extensions", "real"), filepath.Join(root, "extensions", "linked")); err != nil {
			t.Fatal(err)
		}
		_, err := scanExtensions(root)
		if err == nil || !strings.Contains(err.Error(), "symlinked entry") {
			t.Fatalf("err = %v, want the symlink refusal", err)
		}
	})
}

func TestComposedWorkListsMembersSorted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26.5\n\nuse (\n\t./backend\n\t./cli/craft\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, root, "zeta", map[string]string{"go.mod": "module example.test/ext/z\n\ngo 1.26.5\n", "z.go": "package z\n"})
	units, err := scanExtensions(root)
	if err != nil {
		t.Fatal(err)
	}
	work, goVersion, err := composedWork(root, units)
	if err != nil {
		t.Fatal(err)
	}
	if goVersion != "1.26.5" {
		t.Fatalf("go version = %q", goVersion)
	}
	want := "use (\n\t../../backend\n\t../../cli/craft\n\t../../extensions/zeta\n\t./backend\n)\n"
	if !strings.HasSuffix(string(work), want) {
		t.Fatalf("go.work = %q, want use block %q", work, want)
	}
}

// TestDigestTreeIsOrderIndependentAndContentBound: same files → same
// digest; one changed byte → a different one — the property the
// staleness gate rests on.
// TestDigestTreeSkipsInstalledDependencies: a unit's node_modules is resolved
// output, not unit source.
//
// The digest refuses non-regular files so a symlink cannot digest as its
// target's bytes — right for everything a unit author writes, and wrong for
// the tree pnpm builds, which is symlinks all the way down. What pins those
// bytes is the lockfile, so a hash of the unit has no business chasing them,
// and a unit that merely installed its dependencies must not report a
// different identity than the same unit before `pnpm install` ran.
func TestDigestTreeSkipsInstalledDependencies(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "u", map[string]string{
		"go.mod":                "module m\n",
		"frontend/package.json": "{}\n",
		"frontend/screen.tsx":   "export default function S() { return null }\n",
	})
	dir := filepath.Join(root, "extensions", "u")
	before, err := digestTree(dir)
	if err != nil {
		t.Fatalf("digesting the uninstalled unit: %v", err)
	}

	// The shape pnpm actually produces, and the one that used to hard-fail the
	// whole composition the moment a unit had a dependency.
	mods := filepath.Join(dir, "frontend", "node_modules", "react")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(mods, "linked")); err != nil {
		t.Fatal(err)
	}

	after, err := digestTree(dir)
	if err != nil {
		t.Fatalf("a unit with installed dependencies must still digest: %v", err)
	}
	if after != before {
		t.Errorf("digest changed when node_modules appeared (%s -> %s) — installed dependencies are not part of a unit's identity", before, after)
	}
}

// And the refusal it is carved out of still stands everywhere else: a symlink
// among the unit's OWN files is the case the rule was written for.
func TestDigestTreeStillRefusesASymlinkInTheUnitsOwnFiles(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "u", map[string]string{"go.mod": "module m\n"})
	dir := filepath.Join(root, "extensions", "u")
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "sneaky.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := digestTree(dir); err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatalf("err = %v, want the non-regular-file refusal", err)
	}
}

func TestDigestTreeIsOrderIndependentAndContentBound(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "u", map[string]string{"go.mod": "module m\n", "a.go": "package a\n", "sub/b.txt": "b\n"})
	dir := filepath.Join(root, "extensions", "u")
	first, err := digestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := digestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("digest not reproducible: %s vs %s", first, again)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := digestTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("digest unchanged after a content edit")
	}
}

// TestVerifyNoExtraFilesGuardsOnlyTheGeneratedRoot pins both halves of this
// gate's boundary in one place, because they are the same decision seen from
// each side.
//
// Inside build/composition/ nothing rides along: a stale artifact from a
// previous enabled set, or one somebody dropped in, would be compiled into the
// composed binary while composition.json said the tree was current.
//
// Outside it, build/composition-frontend/ is a second composition root that
// openapi-typescript writes, and it must NOT be folded in — this gate's claim is
// that the verified tree holds exactly what the Go generator produced, and a
// Node tool writing into it would break that claim on every run. Asserting the
// exclusion is what stops a later reader "tidying up" the two roots into one.
func TestVerifyNoExtraFilesGuardsOnlyTheGeneratedRoot(t *testing.T) {
	root := t.TempDir()
	outRoot := filepath.Join(root, "build", "composition")
	if err := os.MkdirAll(filepath.Join(outRoot, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{"api/crm.yaml": "sha256:whatever"}
	for _, rel := range []string{"api/crm.yaml", manifestFile} {
		if err := os.WriteFile(filepath.Join(outRoot, filepath.FromSlash(rel)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyNoExtraFiles(root, outputs); err != nil {
		t.Fatalf("a tree holding exactly the outputs plus the manifest was refused: %v", err)
	}

	// The Node lane's root, beside the verified one.
	sibling := filepath.Join(root, "build", "composition-frontend")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "schema.d.ts"), []byte("export {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoExtraFiles(root, outputs); err != nil {
		t.Fatalf("build/composition-frontend/ was pulled into the verified tree: %v — it is a Node-produced root this gate cannot reproduce", err)
	}

	if err := os.WriteFile(filepath.Join(outRoot, "api", "stale.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyNoExtraFiles(root, outputs)
	if err == nil {
		t.Fatal("a file the generation did not write was accepted inside build/composition/")
	}
	if !strings.Contains(err.Error(), "api/stale.yaml") {
		t.Errorf("the refusal does not name the offending file: %v", err)
	}
}
