// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// unitRule is the single source of truth for how ONE backfill task's units and
// floor are computed. Centralizing the three per-task differences here — keyed
// by the generated ai.Task enum — replaces the switch statements that used to
// spread this logic across the estimator, so a new backfill task is a compiler-
// visible map entry rather than three switch arms a change can silently miss.
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
	// count (absorbs batching + solo re-asks), else the summed served calls (one
	// enrich call per person, one embed call per entity).
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

// backfillUnitRules keys the per-task backfill rules by the generated ai.Task
// enum — one exhaustive table for volume, denominator, and floor. The keys ARE
// the priced task set; backfillTasks (below) is the ordered view of them.
var backfillUnitRules = map[ai.Task]unitRule{
	ai.TaskCaptureClassify: {
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
	ai.TaskEnrich: {
		// A zero people_created is "ratio unavailable", not "zero people": the
		// backfill loop never increments the counter (people/orgs are created
		// asynchronously downstream, not at page-commit — see capture/backfill.go
		// RunBackfillStep). Reporting ok=false floors to the named default, which
		// is honest; a silent observed-0 on a consent number — quoting $0 enrich to
		// the user — is not.
		observedUnits: func(scanned int64, y capture.BackfillYields) (int64, bool) {
			if y.PeopleCreated == 0 {
				return 0, false
			}
			return scanned * y.PeopleCreated / y.Scanned, true // persons
		},
		observedDenom: func(slices []ai.ServedTaskTotal, _ int64) int64 { return sumSliceCalls(slices) },
		denomIsCalls:  true, // one enrich call per person
		// Per person: the trailing signature lines plus the extraction prompt in,
		// a small field bundle out.
		floor: ai.Usage{
			TokensIn:  signatureLineCount*signatureLineTokens + enrichSystemTokens,
			TokensOut: enrichFieldsTokens,
		},
	},
	ai.TaskEmbeddings: {
		// person/org embed entities are UNDER-counted while people_created /
		// organizations_created are unpopulated by the backfill loop (they are
		// created asynchronously downstream, not at page-commit), so this degrades
		// to a captured-only figure — a labeled, conservative underestimate.
		// Embeddings is NOT floored: captured is real and dominates the entity mix,
		// so the observed ratio stays the honest anchor.
		observedUnits: func(scanned int64, y capture.BackfillYields) (int64, bool) {
			return scanned * (y.Captured + y.PeopleCreated + y.OrganizationsCreated) / y.Scanned, true // entities
		},
		observedDenom: func(slices []ai.ServedTaskTotal, _ int64) int64 { return sumSliceCalls(slices) },
		denomIsCalls:  true, // one embed call per entity
		// Per entity: input-only — no output, no cache.
		floor: ai.Usage{TokensIn: embedItemTokens},
	},
}

// backfillTasks is the closed set of tasks the backfill preview prices — the
// three AI passes a connect-time backfill drives (ai-operational-spec §2.8/§2.9
// + the embed lane). It is the ordered view of backfillUnitRules' keys, iterated
// in this fixed order for a deterministic estimate; TestEveryBackfillTaskHasAUnitRule
// asserts it matches the map's key set exactly.
var backfillTasks = []ai.Task{ai.TaskCaptureClassify, ai.TaskEnrich, ai.TaskEmbeddings}

// sumSliceCalls totals the served calls across a task's window slices — the
// observed-unit denominator for the tasks that fire one call per unit (enrich
// per person, embeddings per entity).
func sumSliceCalls(slices []ai.ServedTaskTotal) int64 {
	var sum int64
	for _, s := range slices {
		sum += s.Calls
	}
	return sum
}
