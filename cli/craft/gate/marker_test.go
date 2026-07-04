package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnotateThenCollect_roundTripsFindingID(t *testing.T) {
	root := t.TempDir()
	src := "package crmcore\nfunc Person() {\n\tvar data any\n}\n"
	if err := os.WriteFile(filepath.Join(root, "person.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	blocking := []Finding{{
		ID: "f7", File: "person.go", Line: 3, Category: "type-escape-hatch",
		Severity: SeverityBlocker, Confidence: ConfidenceHigh,
		Rationale: "any dodges the type", SuggestedFix: "use crmcore.Person",
	}}
	if err := Annotate(root, blocking); err != nil {
		t.Fatalf("Annotate: %v", err)
	}

	markers, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	got := markers[0]
	if got.Kind != KindFix || got.ID != "f7" {
		t.Errorf("round-trip lost id/kind: got %+v", got)
	}
	if got.Line != 3 { // inserted above the original line 3, taking line 3 itself
		t.Errorf("marker line = %d, want 3 (above the annotated code)", got.Line)
	}

	// The annotated code still contains the original line, shifted down.
	out, _ := os.ReadFile(filepath.Join(root, "person.go"))
	if !strings.Contains(string(out), "var data any") {
		t.Error("annotation must not destroy the original code")
	}
	if !strings.Contains(string(out), "\t// CRAFT-FIX[f7]") {
		t.Errorf("marker not indented to match the code:\n%s", out)
	}
}

func TestAnnotate_multipleFindingsKeepStableLines(t *testing.T) {
	root := t.TempDir()
	src := "line1\nline2\nline3\nline4\n"
	os.WriteFile(filepath.Join(root, "f.go"), []byte(src), 0o644)

	blocking := []Finding{
		{ID: "a", File: "f.go", Line: 2, Category: "over-commenting", Severity: SeverityBlocker, Confidence: ConfidenceHigh, Rationale: "x", SuggestedFix: "y"},
		{ID: "b", File: "f.go", Line: 4, Category: "dead-code", Severity: SeverityBlocker, Confidence: ConfidenceHigh, Rationale: "x", SuggestedFix: "y"},
	}
	if err := Annotate(root, blocking); err != nil {
		t.Fatal(err)
	}
	markers, _ := Collect(root)
	ids := map[string]bool{}
	for _, m := range markers {
		ids[m.ID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Errorf("both markers must survive; got %+v", markers)
	}
}

func TestParseMarker_recognizesBothForms(t *testing.T) {
	fix, ok := parseMarker("\t// CRAFT-FIX[f1] over-commenting (BLOCKER/high): why | FIX: do x")
	if !ok || fix.Kind != KindFix || fix.ID != "f1" {
		t.Errorf("CRAFT-FIX parse failed: %+v ok=%v", fix, ok)
	}
	dis, ok := parseMarker("-- CRAFT-DISPUTE[f2]: this any is at a serialization boundary")
	if !ok || dis.Kind != KindDispute || dis.ID != "f2" {
		t.Errorf("CRAFT-DISPUTE parse failed: %+v ok=%v", dis, ok)
	}
	if dis.Reason != "this any is at a serialization boundary" {
		t.Errorf("dispute reason not recovered: %q", dis.Reason)
	}
	if _, ok := parseMarker("just a normal line of code"); ok {
		t.Error("non-marker line should not parse")
	}
}

func TestCommentDelims_byFileType(t *testing.T) {
	tests := []struct{ path, prefix, suffix string }{
		{"a.go", "// ", ""},
		{"a.ts", "// ", ""},
		{"a.sql", "-- ", ""},
		{"a.css", "/* ", " */"},
	}
	for _, tt := range tests {
		p, s := commentDelims(tt.path)
		if p != tt.prefix || s != tt.suffix {
			t.Errorf("commentDelims(%q) = %q,%q want %q,%q", tt.path, p, s, tt.prefix, tt.suffix)
		}
	}
}
