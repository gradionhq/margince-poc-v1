package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResidue_cleanTreePassesMarkerTreeFails(t *testing.T) {
	root := t.TempDir()
	clean := "package crmcore\nfunc Person() {}\n"
	src := filepath.Join(root, "person.go")
	if err := os.WriteFile(src, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}

	// Clean tree: no markers, gate passes.
	if m, err := Residue(root); err != nil || len(m) != 0 {
		t.Fatalf("clean tree: got %d markers (err %v), want 0", len(m), err)
	}

	// Seed a marker: gate goes red.
	withMarker := "package crmcore\n// CRAFT-FIX[f1] over-commenting (BLOCKER/high): why | FIX: x\nfunc Person() {}\n"
	if err := os.WriteFile(src, []byte(withMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Residue(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].ID != "f1" {
		t.Fatalf("seeded marker: got %+v, want one marker f1", m)
	}

	// Removing the marker (the "fix") returns the gate to green.
	if err := os.WriteFile(src, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, _ := Residue(root); len(m) != 0 {
		t.Fatalf("after removing marker: got %d markers, want 0", len(m))
	}
}

func TestResidue_skipsCraftToolDir(t *testing.T) {
	root := t.TempDir()
	// A file under the tool's own dir that contains a marker token must NOT trip
	// the gate — that source legitimately references the tokens.
	toolFile := filepath.Join(root, CraftToolDir, "gate", "marker.go")
	if err := os.MkdirAll(filepath.Dir(toolFile), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(toolFile, []byte("// CRAFT-FIX[x] is a token this file defines\n"), 0o644)

	if m, err := Residue(root); err != nil || len(m) != 0 {
		t.Fatalf("tool dir should be skipped: got %d markers (err %v)", len(m), err)
	}
}
