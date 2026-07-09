// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package static is the deterministic arm of the code-craftsmanship gate
// (architecture/15, ADR-0045 Am.1). It turns the objectively-decidable rows
// of the anti-tell catalog into AST checks that run before — and cheaper
// than — the heuristic Critic Agent (the gate package). Every finding it
// emits is high-confidence by construction, so a BLOCKER here is safe to
// merge-block with no human override. Exposed as `craft static`.
package static

import "fmt"

// Severity mirrors architecture/15 §4. Only BLOCKER stops a merge; MAJOR and
// MINOR are reported but non-blocking (unless the caller opts into -strict).
type Severity int

// The three rungs, weakest first so Blocker compares greatest.
const (
	Minor Severity = iota
	Major
	Blocker
)

func (s Severity) String() string {
	switch s {
	case Blocker:
		return "BLOCKER"
	case Major:
		return "MAJOR"
	default:
		return "MINOR"
	}
}

// Finding is one craftsmanship violation, anchored to a line so the build
// agent (and a human reviewer) can jump straight to it. Confidence is always
// "high" — this arm only carries checks that are objectively decidable from
// the syntax tree; the fuzzy calls live in the Critic Agent.
type Finding struct {
	Check    string   `json:"check"`
	Severity Severity `json:"-"`
	Level    string   `json:"severity"`
	Conf     string   `json:"confidence"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Message  string   `json:"message"`
}

func newFinding(check string, sev Severity, path string, line int, msgf string, args ...any) Finding {
	return Finding{
		Check:    check,
		Severity: sev,
		Level:    sev.String(),
		Conf:     "high",
		File:     path,
		Line:     line,
		Message:  fmt.Sprintf(msgf, args...),
	}
}

// Report is the whole run: the findings plus the merge verdict the caller
// turns into an exit code. The JSON shape matches the Critic Agent's result,
// so both arms of the gate feed one downstream consumer.
type Report struct {
	Tool     string    `json:"tool"`
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
}
