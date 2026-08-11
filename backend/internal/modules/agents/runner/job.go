// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

// What a run is GIVEN: the goal, its seed grounding, the budget that bounds it,
// and the catalog entry's allowlist it inherits. Authority is deliberately not
// here — it rides the context principal, the same way every other surface
// carries it.

// Budget bounds one run (architecture/07 §4). Both are HARD per-run
// ceilings, deliberately independent of workspace-level budgets: one
// unattended run can never claim the whole workspace budget (RT-AI-H5).
type Budget struct {
	MaxSteps        int
	MaxOutputTokens int
}

// The §4 RATIFY defaults: 40 reason-act cycles sized to one deal-bundle
// pass, 50k output tokens per run.
const (
	DefaultMaxSteps        = 40
	DefaultMaxOutputTokens = 50_000
)

func (b Budget) withDefaults() Budget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultMaxSteps
	}
	if b.MaxOutputTokens <= 0 {
		b.MaxOutputTokens = DefaultMaxOutputTokens
	}
	return b
}

// Job is one runner invocation: a goal over seed grounding under a
// budget. Authority is NOT here — it rides the context principal, the
// same way every other surface carries it.
type Job struct {
	Goal       string
	TriggerRef string
	Grounding  []Grounding
	Budget     Budget
	// Tools is the catalog entry's allowlist, carried from AgentSpec.Tools.
	//
	// EMPTY MEANS NO NARROWING, and that default is deliberate rather than
	// lazy: the certification lane builds a Job with no spec behind it, and
	// a caller that is not a catalog agent has no allowlist to apply. It is
	// safe HERE because it can only widen back to what the passport already
	// admits — but it is the same "empty means everything" reading
	// AgentSpec.Tools refuses, so the two seams are held to different rules
	// on purpose.
	//
	// The invariant that makes the difference safe is a RUNTIME one, not a
	// property of any test: A JOB BUILT FROM A CATALOG ENTRY CARRIES THAT
	// ENTRY'S OWN ALLOWLIST. A scheduled agent therefore never reaches the
	// empty case; only a caller with no entry behind it does.
	Tools []string
}

// Grounding is one provenance-stamped seed context item (§3): T2
// content is spotlighted as data-not-instructions before it enters the
// prompt.
type Grounding struct {
	SourceID  string
	TrustTier string // "T0" | "T1" | "T2"
	Content   string
}
