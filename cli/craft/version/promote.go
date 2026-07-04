package version

import (
	"fmt"

	"github.com/gradionhq/margince/cli/craft/golden"
)

// Promotion governs how the gate's version tuple changes: a candidate is adopted
// ONLY if it passes the golden-set eval without regressing BLOCK precision below
// the active version's. A regression auto-rolls-back to the current pinned tuple.
// This is the structural reason the gate can learn aggressively yet stay safe to
// leave merge-blocking (architecture/17 §4).

// Promotion is the decision: which tuple is now in force, and why.
type Promotion struct {
	Active   Tuple
	Promoted bool
	Reason   string
}

// Decide adopts candidate iff its eval passed AND its BLOCK precision does not
// regress below baselinePrecision (the active version's). Otherwise it keeps the
// current tuple — the auto-rollback. baselinePrecision is the active version's
// last known precision (typically 1.0 for a no-override gate).
func Decide(current, candidate Tuple, candidateReport golden.EvalReport, baselinePrecision float64) Promotion {
	switch {
	case !candidateReport.Pass:
		return Promotion{
			Active:   current,
			Promoted: false,
			Reason: fmt.Sprintf("rollback: candidate eval failed (precision %.3f, %d regressed case(s))",
				candidateReport.Metrics.BlockPrecision, len(candidateReport.Mismatches)),
		}
	case candidateReport.Metrics.BlockPrecision < baselinePrecision:
		return Promotion{
			Active:   current,
			Promoted: false,
			Reason: fmt.Sprintf("rollback: BLOCK precision regressed %.3f -> %.3f",
				baselinePrecision, candidateReport.Metrics.BlockPrecision),
		}
	default:
		return Promotion{
			Active:   candidate,
			Promoted: true,
			Reason:   fmt.Sprintf("promoted: precision %.3f (>= %.3f), no regressions", candidateReport.Metrics.BlockPrecision, baselinePrecision),
		}
	}
}
