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
	if !bytes.Equal(stub, extensionsGen(nil)) {
		t.Fatalf("composition/extensions_gen.go differs from the generator's vanilla output:\n--- stub ---\n%s\n--- generated ---\n%s", stub, extensionsGen(nil))
	}
}

func TestExtensionsGenWiresUnitsInSortedOrder(t *testing.T) {
	got := string(extensionsGen([]extensionUnit{
		{Name: "alpha", ModulePath: "example.test/ext/alpha"},
		{Name: "beta", ModulePath: "example.test/ext/beta"},
	}))
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
			if _, err := canonicalGoSource("extensions_gen.go", extensionsGen(units)); err != nil {
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
		{name: "unbuilt capability layer", unit: "with-api", files: map[string]string{"go.mod": goMod, "a.go": "package a\n", "api/api.yaml": "{}\n"}, wantErr: "api/ composition is not built yet"},
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
