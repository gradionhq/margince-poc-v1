package gate

// The canonical review output. The JSON is the source of truth; the in-source
// markers (B-EP11.4) and PR comments derive from it. Validation + the
// BLOCK-eligibility mapping land in B-EP11.2b (verdict.go).

// Severity ranks how serious a finding is, from BLOCKER down to MINOR.
type Severity string

const (
	// SeverityBlocker is a finding that must block the merge.
	SeverityBlocker Severity = "BLOCKER"
	// SeverityMajor is a serious finding that does not by itself block.
	SeverityMajor Severity = "MAJOR"
	// SeverityMinor is a low-stakes nit.
	SeverityMinor Severity = "MINOR"
)

// Confidence is the reviewer's certainty in a finding.
type Confidence string

const (
	// ConfidenceHigh marks a finding the reviewer is confident in.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium marks a finding the reviewer is moderately sure of.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow marks a speculative finding.
	ConfidenceLow Confidence = "low"
)

// Verdict is the gate's merge decision for a review.
type Verdict string

const (
	// VerdictPass lets the merge proceed.
	VerdictPass Verdict = "PASS"
	// VerdictBlock keeps the merge blocked.
	VerdictBlock Verdict = "BLOCK"
)

// Finding is one craftsmanship issue anchored to a source location.
type Finding struct {
	ID           string     `json:"id"`
	File         string     `json:"file"`
	Line         int        `json:"line"`
	Category     string     `json:"category"`
	Severity     Severity   `json:"severity"`
	Confidence   Confidence `json:"confidence"`
	Rationale    string     `json:"rationale"`
	SuggestedFix string     `json:"suggested_fix"`
}

// Result is the full review output. GateVersion pins the identity tuple
// (prompt, rubric, exemplar-set, model) so any verdict is reproducible (B-EP11.8a).
type Result struct {
	GateVersion string    `json:"gate_version"`
	Verdict     Verdict   `json:"verdict"`
	Findings    []Finding `json:"findings"`
	// Scratchpad is the agent's reasoning trace. It is informational — never the
	// basis for the verdict, which is computed from findings in code (B-EP11.2b).
	Scratchpad string `json:"scratchpad,omitempty"`
}
