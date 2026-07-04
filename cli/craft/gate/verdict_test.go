package gate

import (
	"testing"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

func TestComputeVerdict_blocksOnlyOnHighConfidenceBlockEligibleBlocker(t *testing.T) {
	r, err := rubric.Load()
	if err != nil {
		t.Fatalf("load rubric: %v", err)
	}
	tests := []struct {
		name    string
		finding Finding
		want    Verdict
	}{
		{"high-confidence blocker in block-eligible category", Finding{Severity: SeverityBlocker, Confidence: ConfidenceHigh, Category: "over-commenting"}, VerdictBlock},
		{"blocker but only medium confidence", Finding{Severity: SeverityBlocker, Confidence: ConfidenceMedium, Category: "over-commenting"}, VerdictPass},
		{"high confidence but only major", Finding{Severity: SeverityMajor, Confidence: ConfidenceHigh, Category: "over-commenting"}, VerdictPass},
		{"high-confidence blocker in a non-block-eligible category", Finding{Severity: SeverityBlocker, Confidence: ConfidenceHigh, Category: "restraint"}, VerdictPass},
		{"minor", Finding{Severity: SeverityMinor, Confidence: ConfidenceLow, Category: "over-commenting"}, VerdictPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeVerdict([]Finding{tt.finding}, r); got != tt.want {
				t.Errorf("ComputeVerdict = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeVerdict_emptyFindingsPass(t *testing.T) {
	r, _ := rubric.Load()
	if got := ComputeVerdict(nil, r); got != VerdictPass {
		t.Errorf("no findings => %v, want PASS", got)
	}
}

func TestValidate_rejectsMalformedOutput(t *testing.T) {
	good := Finding{ID: "f1", File: "a.go", Line: 4, Category: "over-commenting", Severity: SeverityBlocker, Confidence: ConfidenceHigh, Rationale: "r", SuggestedFix: "x"}
	tests := []struct {
		name    string
		res     Result
		wantErr bool
	}{
		{"valid", Result{GateVersion: "v1", Verdict: VerdictBlock, Findings: []Finding{good}}, false},
		{"empty gate version", Result{Verdict: VerdictPass}, true},
		{"unknown verdict", Result{GateVersion: "v1", Verdict: "MAYBE"}, true},
		{"finding missing id", Result{GateVersion: "v1", Verdict: VerdictPass, Findings: []Finding{{File: "a.go", Line: 1, Category: "c", Severity: SeverityMinor, Confidence: ConfidenceLow, Rationale: "r", SuggestedFix: "x"}}}, true},
		{"invalid severity", Result{GateVersion: "v1", Verdict: VerdictPass, Findings: []Finding{{ID: "f", File: "a.go", Line: 1, Category: "c", Severity: "HUGE", Confidence: ConfidenceLow, Rationale: "r", SuggestedFix: "x"}}}, true},
		{"line below 1", Result{GateVersion: "v1", Verdict: VerdictPass, Findings: []Finding{{ID: "f", File: "a.go", Line: 0, Category: "c", Severity: SeverityMinor, Confidence: ConfidenceLow, Rationale: "r", SuggestedFix: "x"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(&tt.res)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
