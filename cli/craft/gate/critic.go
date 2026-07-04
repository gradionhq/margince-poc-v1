package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

// Reviewer runs the craftsmanship review. Each Review is a fresh session: one
// stateless completion with no memory of prior PRs, so a verdict depends only on
// the pinned (prompt, rubric, exemplars, model) tuple and the PR under review.
type Reviewer struct {
	client      Client
	rubric      *rubric.Rubric
	gateVersion string
}

// NewReviewer returns a Reviewer bound to the client, rubric, and gate version
// that together pin the verdict's reproducibility tuple.
func NewReviewer(c Client, r *rubric.Rubric, gateVersion string) *Reviewer {
	return &Reviewer{client: c, rubric: r, gateVersion: gateVersion}
}

// Review assembles the inputs, runs one fresh-session completion, and returns the
// parsed result with the gate version stamped on. The verdict the model returns
// is advisory; B-EP11.2b recomputes the merge-blocking verdict from the findings.
func (rv *Reviewer) Review(ctx context.Context, in Inputs) (*Result, error) {
	out, err := rv.client.Complete(ctx, buildPrompt(rv.rubric, in))
	if err != nil {
		return nil, fmt.Errorf("review completion: %w", err)
	}
	res, err := parseResult(out)
	if err != nil {
		return nil, err
	}
	res.GateVersion = rv.gateVersion
	// The verdict is decided in code from the calibrated rule, never trusted from
	// the model (B-EP11.2b). Validate first so bad output fails loud, not silent-PASS.
	if err := Validate(res); err != nil {
		return nil, err
	}
	res.Verdict = ComputeVerdict(res.Findings, rv.rubric)
	return res, nil
}

// parseResult extracts the JSON result from the model's output, tolerating a
// ```json fence or leading/trailing prose.
func parseResult(out string) (*Result, error) {
	raw := extractJSON(out)
	if raw == "" {
		return nil, fmt.Errorf("no JSON object in model output")
	}
	var res Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("parse result JSON: %w", err)
	}
	return &res, nil
}

// extractJSON returns the outermost {...} span, ignoring any surrounding fence or
// prose. It scans for balanced braces outside of string literals.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
