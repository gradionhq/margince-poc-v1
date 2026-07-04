package gate

import (
	"fmt"
	"strings"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

// The BLOCK rule lives in code, not in the prompt: the model classifies findings,
// but whether they block a merge is decided here. This is what keeps a no-override
// hard block safe — a model that over-eagerly says "BLOCK" cannot wedge the
// pipeline, because the verdict is recomputed from the calibrated rule.

// ComputeVerdict returns BLOCK iff at least one finding is a high-confidence
// BLOCKER in a block-eligible category. Everything below that bar is non-blocking.
func ComputeVerdict(findings []Finding, r *rubric.Rubric) Verdict {
	for _, f := range findings {
		if f.Severity == SeverityBlocker && f.Confidence == ConfidenceHigh && r.BlockEligible(f.Category) {
			return VerdictBlock
		}
	}
	return VerdictPass
}

// Blocking returns the findings that caused a BLOCK — the ones the annotator
// (B-EP11.4) materializes as CRAFT-FIX markers.
func Blocking(findings []Finding, r *rubric.Rubric) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Severity == SeverityBlocker && f.Confidence == ConfidenceHigh && r.BlockEligible(f.Category) {
			out = append(out, f)
		}
	}
	return out
}

var (
	validSeverities  = map[Severity]bool{SeverityBlocker: true, SeverityMajor: true, SeverityMinor: true}
	validConfidences = map[Confidence]bool{ConfidenceHigh: true, ConfidenceMedium: true, ConfidenceLow: true}
)

// Validate rejects malformed output: a missing gate version, an unknown verdict,
// or a finding missing any required field. The gate fails loud on bad model
// output rather than silently passing it.
func Validate(res *Result) error {
	var problems []string
	if res.GateVersion == "" {
		problems = append(problems, "gate_version is empty")
	}
	if res.Verdict != VerdictPass && res.Verdict != VerdictBlock {
		problems = append(problems, fmt.Sprintf("verdict %q is not PASS or BLOCK", res.Verdict))
	}
	seen := map[string]bool{}
	for i, f := range res.Findings {
		where := fmt.Sprintf("findings[%d]", i)
		if f.ID == "" {
			problems = append(problems, where+": id is empty")
		} else if seen[f.ID] {
			problems = append(problems, where+": duplicate id "+f.ID)
		}
		seen[f.ID] = true
		if f.File == "" {
			problems = append(problems, where+": file is empty")
		}
		if f.Line < 1 {
			problems = append(problems, where+": line must be >= 1")
		}
		if f.Category == "" {
			problems = append(problems, where+": category is empty")
		}
		if !validSeverities[f.Severity] {
			problems = append(problems, where+fmt.Sprintf(": invalid severity %q", f.Severity))
		}
		if !validConfidences[f.Confidence] {
			problems = append(problems, where+fmt.Sprintf(": invalid confidence %q", f.Confidence))
		}
		if f.Rationale == "" {
			problems = append(problems, where+": rationale is empty")
		}
		if f.SuggestedFix == "" {
			problems = append(problems, where+": suggested_fix is empty")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid review result: %s", strings.Join(problems, "; "))
	}
	return nil
}
