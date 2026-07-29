// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// unitRule is the single source of truth for how ONE unit rule's units and
// floor are computed. Centralizing the per-rule differences here — keyed by the
// contract's cost-unit rule NAME — replaces the switch statements that used to
// spread this logic across the estimator, so a new priced task is a contract
// entry naming a rule rather than three switch arms a change can silently miss.
// TestEveryBackfillTaskHasAUnitRule is the build guardrail: it fails if a
// backfill task lacks a rule (or a rule is incomplete).
type unitRule struct {
	// observedUnits computes the expected units for scanned messages from a
	// COMPLETED backfill yield (the caller guarantees y.Scanned > 0). ok=false
	// means the yield cannot anchor this task's ratio, so the caller floors and
	// marks the estimate heuristic — this is where enrich's C1 guard lives.
	observedUnits func(scanned int64, y capture.BackfillYields) (units int64, ok bool)
	// observedDenom is the observed-unit count the window's served slices are
	// divided by for the priced-slice cost: classify's exact labeled-message
	// count (absorbs batching + solo re-asks), else the summed COMPLETED served
	// calls (one enrich call per person, one embed call per entity). Completed —
	// not all served — so a metering_failed retry, whose spend rides the token
	// numerator, does not inflate the call denominator and divide its own cost out.
	observedDenom func(slices []ai.ServedTaskTotal, labeled int64) int64
	// denomIsCalls says whether observedDenom is a COUNT OF CALLS (Σcalls) rather
	// than a count of some other unit. It decides how a partly-unpriced mix
	// re-weights: when the denominator is call-based (enrich per person, embed
	// per entity — one call per unit), the priced slices' share of the cost is
	// their share of the calls, so pricedDenom scales by pricedCalls/Σcalls.
	// When it is NOT call-based (classify's denominator is labeled MESSAGES, and
	// one call is a variable-size batch), that call-fraction reweight overquotes:
	// a 10-message priced batch and a 1-message unpriced retry are 1 call each,
	// so a 50/50 call split would double the per-message cost. For those tasks
	// the priced cost is spread across the FULL observed denominator and the
	// unpriced share falls to $0 (already flagged heuristic) — no reweight.
	denomIsCalls bool
	// floor is the per-UNIT token means for the cold-start work-shape floor,
	// derived from the real prompt shape (the constants + rationale in floor.go).
	// Non-zero for every backfill task (asserted by the fitness test).
	floor ai.Usage
}

// unitRulesByName keys the backfill rules by the contract's cost-unit rule name
// — one table for volume, denominator, and floor. The contract says WHICH rule
// prices a task; this package says what the rule computes, so neither half can
// drift without the other failing the build.
var unitRulesByName = map[string]unitRule{
	"per_message": {
		// units = captured messages, scaled from the yield's captured/scanned ratio.
		observedUnits: func(scanned int64, y capture.BackfillYields) (int64, bool) {
			return scanned * y.Captured / y.Scanned, true // messages
		},
		observedDenom: func(_ []ai.ServedTaskTotal, labeled int64) int64 { return labeled },
		denomIsCalls:  false, // labeled MESSAGES, not calls: a call is a variable-size batch
		// Per message: the truncated body plus the batch system/schema prompt
		// amortized across the batch; one short verdict out.
		floor: ai.Usage{
			TokensIn:  classifyBodyLimit/charsPerToken + classifySystemTokens/classifyBatchSize,
			TokensOut: classifyVerdictTokens,
		},
	},
	"per_person": {
		// A zero people_created is "ratio unavailable", not "zero people": a run
		// counts only the counterparties its own pages minted, so a window whose
		// senders were all already known, suppressed, or deferred to the verdict
		// engine reads zero while a wider window would still create plenty. Reporting
		// ok=false floors to the named default, which is honest; a silent
		// observed-0 on a consent number — quoting $0 enrich to the user — is not.
		observedUnits: func(scanned int64, y capture.BackfillYields) (int64, bool) {
			if y.PeopleCreated == 0 {
				return 0, false
			}
			return scanned * y.PeopleCreated / y.Scanned, true // persons
		},
		observedDenom: func(slices []ai.ServedTaskTotal, _ int64) int64 { return sumCompletedCalls(slices) },
		denomIsCalls:  true, // one enrich call per person
		// Per person: the trailing signature lines plus the extraction prompt in,
		// a small field bundle out.
		floor: ai.Usage{
			TokensIn:  signatureLineCount*signatureLineTokens + enrichSystemTokens,
			TokensOut: enrichFieldsTokens,
		},
	},
	"per_entity": {
		// person/org embed entities are counted from the run's own committed
		// yields, which are an honest UNDER-count: a sender the tier gate deferred
		// is resolved by the verdict engine long after the page that saw it, and
		// the person it may eventually mint is nobody's page to claim. Embeddings
		// is NOT floored on that shortfall: captured is exact and dominates the
		// entity mix, so the observed ratio stays the honest anchor.
		observedUnits: func(scanned int64, y capture.BackfillYields) (int64, bool) {
			return scanned * (y.Captured + y.PeopleCreated + y.OrganizationsCreated) / y.Scanned, true // entities
		},
		observedDenom: func(slices []ai.ServedTaskTotal, _ int64) int64 { return sumCompletedCalls(slices) },
		denomIsCalls:  true, // one embed call per entity
		// Per entity: input-only — no output, no cache.
		floor: ai.Usage{TokensIn: embedItemTokens},
	},
}

// unitRuleFor resolves the rule the contract names for a task. The bool is
// false for an unpriced task — which is honest, not an error: cost stays
// transparency, never a gate.
func unitRuleFor(task ai.Task) (unitRule, bool) {
	name := ai.CostUnitFor(task)
	if name == "" && task == ai.TaskEmbeddings {
		// embed is a contract section, not a task, so its rule is named there.
		name = ai.EmbedCostUnit()
	}
	if name == "" {
		return unitRule{}, false
	}
	rule, ok := unitRulesByName[name]
	return rule, ok
}

// backfillTasks is the closed set of tasks the backfill preview prices — the
// three AI passes a connect-time backfill drives (ai-operational-spec §2.8/§2.9
// + the embed lane). It is iterated in this fixed order for a deterministic
// estimate; TestEveryBackfillTaskHasAUnitRule asserts it matches the set of
// tasks the contract prices exactly.
var backfillTasks = []ai.Task{ai.TaskCaptureClassify, ai.TaskEnrich, ai.TaskEmbeddings}

// sumCompletedCalls totals the COMPLETED served calls (error_sentinel IS NULL,
// excluding metering_failed retries) across a task's window slices — the
// observed-unit denominator for the tasks that fire one call per unit (enrich
// per person, embeddings per entity). A metering_failed retry spent tokens
// (carried in the token sums) but completed no fresh unit, so it must not be
// counted here: doing so would inflate the denominator and cancel its own cost.
func sumCompletedCalls(slices []ai.ServedTaskTotal) int64 {
	var sum int64
	for _, s := range slices {
		sum += s.CompletedCalls
	}
	return sum
}
