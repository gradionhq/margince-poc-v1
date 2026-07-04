package version

import "testing"

func TestCurrent_isDeterministicAndStampsAllFourParts(t *testing.T) {
	a, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	b, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a.String() != b.String() {
		t.Errorf("Current is not deterministic: %q vs %q", a, b)
	}
	if a.Prompt == "" || a.Rubric == "" || a.ExemplarSet == "" || a.Model == "" {
		t.Errorf("gate_version is missing a part: %+v", a)
	}
}

func TestCurate_curatedNotBlindlyAppended(t *testing.T) {
	set, err := LoadExemplars()
	if err != nil {
		t.Fatalf("load exemplars: %v", err)
	}
	n := len(set.Exemplars)

	// A genuine new exemplar with provenance is accepted.
	good := Exemplar{ID: "ex-new", Polarity: "negative", Category: "premature-abstraction", Code: "x", Rationale: "r", Provenance: "adjudication:42"}
	set2, err := Curate(set, good)
	if err != nil || len(set2.Exemplars) != n+1 {
		t.Fatalf("good candidate rejected: %v (len %d)", err, len(set2.Exemplars))
	}

	// No provenance is rejected.
	if _, err := Curate(set, Exemplar{ID: "ex-noprov", Code: "x"}); err == nil {
		t.Error("exemplar without provenance must be rejected")
	}

	// A duplicate id is rejected.
	dup := set.Exemplars[0]
	if _, err := Curate(set, dup); err == nil {
		t.Error("duplicate exemplar id must be rejected")
	}
}
