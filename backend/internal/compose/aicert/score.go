// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"fmt"
	"slices"
)

// Verdict strings — the three §5 outcomes a run set can land on.
const (
	VerdictCertified         = "certified"
	VerdictSupportedDegraded = "supported_degraded"
	VerdictNotSupported      = "not_supported"
)

// RunResult is one candidate completion already scored and measured by the
// caller (the runner drives the model and the judge; this package never
// calls either). Degraded mirrors the router's own degrade signal (a
// budget-forced tier drop mid-run); HardPass is the run's pass/fail verdict
// against its scenario's structural checks, caps, and judge score — the
// input Verdict folds across a whole run set.
type RunResult struct {
	Output    string
	LatencyMS int64
	Tokens    int
	Degraded  bool
	HardPass  bool
	Score     int
}

// Verdict folds N runs of one scenario into a certification outcome per
// spec §5, literally:
//
//	Certified          = every run HardPass ∧ median(Score) ≥ b.CertifiedMin ∧ min(Score) ≥ b.Floor
//	Supported-degraded = ≥⌈2N/3⌉ runs HardPass ∧ median(Score) ≥ b.DegradedMin
//	otherwise          = Not-supported
//
// reliability is the fraction of runs that HardPassed (0..1), reported
// regardless of which verdict the run set lands on — it is the number a
// dashboard trends over time, not just the pass/fail label.
//
// Verdict requires an ODD run count (N=0 or even panics): a median needs a
// single middle element, and the runner's own config (RunnerConfig.Repeats)
// already enforces oddness before any run happens, so a call here with an
// even N is a caller bug, not a certification input to report gracefully.
func Verdict(rs []RunResult, b Bands) (verdict string, reliability float64) {
	n := len(rs)
	if n == 0 || n%2 == 0 {
		panic(fmt.Sprintf("aicert: Verdict: run count must be odd and non-zero, got %d", n))
	}

	passed := 0
	scores := make([]int, n)
	for i, r := range rs {
		scores[i] = r.Score
		if r.HardPass {
			passed++
		}
	}
	reliability = float64(passed) / float64(n)

	slices.Sort(scores)
	median := scores[n/2]
	minScore := scores[0]

	if passed == n && median >= b.CertifiedMin && minScore >= b.Floor {
		return VerdictCertified, reliability
	}
	// ceil(2N/3) via integer arithmetic: (2N + 2) / 3.
	degradedThreshold := (2*n + 2) / 3
	if passed >= degradedThreshold && median >= b.DegradedMin {
		return VerdictSupportedDegraded, reliability
	}
	return VerdictNotSupported, reliability
}
