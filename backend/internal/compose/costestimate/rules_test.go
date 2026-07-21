// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// The build guardrail for the enum-keyed rule table: every backfill task the
// estimator prices must carry a complete, non-zero rule, and the ordered
// backfillTasks slice must match the map's key set exactly. A backfill task
// added without a rule — or a rule the ordered slice forgets — fails the build
// here rather than silently pricing that task at zero.
func TestEveryBackfillTaskHasAUnitRule(t *testing.T) {
	for _, task := range backfillTasks {
		rule, ok := backfillUnitRules[task]
		if !ok {
			t.Fatalf("backfillTasks lists %s but backfillUnitRules has no rule for it", task)
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

	// Set equality both ways: no rule the ordered slice omits, no slice entry the
	// map lacks. The size check also catches a duplicated slice entry.
	if len(backfillUnitRules) != len(backfillTasks) {
		t.Fatalf("backfillUnitRules has %d rules but backfillTasks lists %d — they must match exactly",
			len(backfillUnitRules), len(backfillTasks))
	}
	listed := make(map[ai.Task]bool, len(backfillTasks))
	for _, task := range backfillTasks {
		listed[task] = true
	}
	for task := range backfillUnitRules {
		if !listed[task] {
			t.Fatalf("backfillUnitRules has a rule for %s that backfillTasks does not list", task)
		}
	}
}
