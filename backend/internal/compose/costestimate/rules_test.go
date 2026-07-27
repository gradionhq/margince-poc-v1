// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// The build guardrail for the rule table: every backfill task the estimator
// prices must resolve to a complete, non-zero rule, and the ordered
// backfillTasks slice must match the set of tasks the contract prices exactly.
// A priced task the ordered slice forgets — or a slice entry the contract does
// not price — fails the build here rather than silently pricing that task at
// zero.
func TestEveryBackfillTaskHasAUnitRule(t *testing.T) {
	for _, task := range backfillTasks {
		rule, ok := unitRuleFor(task)
		if !ok {
			t.Fatalf("backfillTasks lists %s but no rule resolves for it", task)
		}
		if rule.observedUnits == nil {
			t.Fatalf("rule[%s].observedUnits is nil — the observed-volume ratio is missing", task)
		}
		if rule.observedDenom == nil {
			t.Fatalf("rule[%s].observedDenom is nil — the observed-unit denominator is missing", task)
		}
		if rule.floor == (ai.Usage{}) {
			t.Fatalf("rule[%s].floor is the zero Usage — every backfill task needs a non-zero work-shape floor", task)
		}
	}

	// The priced set comes off the contract, not off this package's own table:
	// embeddings is priced by the contract's embed section rather than a task
	// entry, so it is added the same way unitRuleFor resolves it.
	priced := map[ai.Task]bool{ai.TaskEmbeddings: true}
	for _, task := range ai.AllTasks() {
		if ai.CostUnitFor(task) != "" {
			priced[task] = true
		}
	}

	// Set equality both ways: no priced task the ordered slice omits, no slice
	// entry the contract does not price. The size check also catches a
	// duplicated slice entry.
	if len(priced) != len(backfillTasks) {
		t.Fatalf("the contract prices %d tasks but backfillTasks lists %d — they must match exactly",
			len(priced), len(backfillTasks))
	}
	listed := make(map[ai.Task]bool, len(backfillTasks))
	for _, task := range backfillTasks {
		listed[task] = true
	}
	for task := range priced {
		if !listed[task] {
			t.Fatalf("the contract prices %s but backfillTasks does not list it", task)
		}
	}
}

// The contract names which rule prices a task; this package owns the
// arithmetic. Both directions must hold, or the estimator silently prices
// nothing for a task the contract says is priced.
func TestEveryContractCostUnitHasARule(t *testing.T) {
	for _, task := range ai.AllTasks() {
		name := ai.CostUnitFor(task)
		if name == "" {
			continue
		}
		if _, ok := unitRulesByName[name]; !ok {
			t.Errorf("task %q names cost_unit %q, which no rule implements", task, name)
		}
	}
	if _, ok := unitRulesByName[ai.EmbedCostUnit()]; !ok {
		t.Errorf("embed names cost_unit %q, which no rule implements", ai.EmbedCostUnit())
	}
}

// And the reverse: a rule nothing names is dead code that reads as coverage.
func TestEveryRuleIsNamedByTheContract(t *testing.T) {
	named := map[string]bool{ai.EmbedCostUnit(): true}
	for _, task := range ai.AllTasks() {
		if n := ai.CostUnitFor(task); n != "" {
			named[n] = true
		}
	}
	for name := range unitRulesByName {
		if !named[name] {
			t.Errorf("rule %q is implemented but no contract entry names it", name)
		}
	}
}

// Every priced task keeps a non-zero work-shape floor: an unpriced observation
// must degrade to a floor, never to a silent zero on a consent number.
func TestEveryPricedTaskHasAFloor(t *testing.T) {
	for _, task := range backfillTasks {
		rule, ok := unitRuleFor(task)
		if !ok {
			t.Errorf("backfill task %q has no unit rule", task)
			continue
		}
		if rule.floor.TokensIn == 0 {
			t.Errorf("task %q has a zero-token floor", task)
		}
	}
}
