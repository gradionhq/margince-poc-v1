// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The runner's small deterministic helpers, tested away from the router: how
// many repeats a run gets, how a per-task ladder override is applied, how the
// corpus is grouped, and the two folds a record's numbers come out of. None of
// them calls a model, so none of them needs one to be wrong in a way a reader
// would notice.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestRepeatsOrDefault(t *testing.T) {
	cases := []struct {
		name    string
		in      int
		want    int
		wantErr bool
	}{
		{"zero defaults to three", 0, 3, false},
		{"valid odd", 5, 5, false},
		{"one is valid", 1, 1, false},
		{"even is refused", 4, 0, true},
		{"negative is refused", -1, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repeatsOrDefault(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("repeatsOrDefault(%d): want an error, got %d", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("repeatsOrDefault(%d): unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("repeatsOrDefault(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestOverrideForTaskRebindsOnlyTheTaskLadderAndNeverMutatesBase(t *testing.T) {
	base := ai.FakeRoutingConfig()
	before := len(base.Tiers)

	overridden, err := overrideForTask(base, ai.TaskColdStart, "anthropic:claude-cert-test")
	if err != nil {
		t.Fatalf("valid override rejected: %v", err)
	}
	for _, tier := range ai.TaskLadder(ai.TaskColdStart) {
		binding := overridden.Tiers[tier]
		if binding.Provider != "anthropic" || binding.Model != "claude-cert-test" {
			t.Errorf("tier %s = %+v, want the override binding", tier, binding)
		}
	}
	if binding := overridden.Tiers[ai.TierLocalSmall]; binding.Provider != ai.ProviderFake {
		t.Errorf("a tier off TaskColdStart's ladder must be untouched, got %+v", binding)
	}
	if len(base.Tiers) != before || base.Tiers[ai.TierCheapCloud].Provider != ai.ProviderFake {
		t.Fatalf("overrideForTask mutated the base config's own tier map: %+v", base.Tiers)
	}
}

func TestOverrideForTaskNoOpOnEmptyOverride(t *testing.T) {
	base := ai.FakeRoutingConfig()
	got, err := overrideForTask(base, ai.TaskColdStart, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tiers[ai.TierCheapCloud].Provider != ai.ProviderFake {
		t.Fatalf("empty override must leave the base binding untouched, got %+v", got.Tiers[ai.TierCheapCloud])
	}
}

func TestOverrideForTaskRefusesAMalformedOverride(t *testing.T) {
	_, err := overrideForTask(ai.FakeRoutingConfig(), ai.TaskColdStart, "no-colon-here")
	if err == nil || !strings.Contains(err.Error(), "provider:model") {
		t.Fatalf("want a provider:model complaint, got %v", err)
	}
}

func TestOverrideForTaskRefusesATaskWithNoLadder(t *testing.T) {
	_, err := overrideForTask(ai.FakeRoutingConfig(), ai.Task("not_a_real_task"), "anthropic:claude-x")
	if err == nil || !strings.Contains(err.Error(), "no routing ladder") {
		t.Fatalf("want a no-routing-ladder complaint, got %v", err)
	}
}

func TestGroupByTaskFiltersAndSortedTasksOrdersDeterministically(t *testing.T) {
	scenarios := []Scenario{
		{Name: "a", Task: string(ai.TaskSummarize)},
		{Name: "b", Task: string(ai.TaskColdStart)},
		{Name: "c", Task: string(ai.TaskSummarize)},
	}
	all := groupByTask(scenarios, "")
	if len(all[ai.TaskSummarize]) != 2 || len(all[ai.TaskColdStart]) != 1 {
		t.Fatalf("unfiltered grouping = %+v", all)
	}
	filtered := groupByTask(scenarios, string(ai.TaskColdStart))
	if len(filtered) != 1 || len(filtered[ai.TaskColdStart]) != 1 {
		t.Fatalf("filtered grouping = %+v", filtered)
	}
	order := sortedTasks(all)
	if len(order) != 2 || order[0] != ai.TaskColdStart || order[1] != ai.TaskSummarize {
		t.Fatalf("sortedTasks = %v, want [cold_start summarize]", order)
	}
}

func TestWorstVerdictRanksNotSupportedBelowDegradedBelowCertified(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{VerdictCertified, VerdictNotSupported, VerdictNotSupported},
		{VerdictCertified, VerdictSupportedDegraded, VerdictSupportedDegraded},
		{VerdictSupportedDegraded, VerdictNotSupported, VerdictNotSupported},
		{VerdictCertified, VerdictCertified, VerdictCertified},
	}
	for _, c := range cases {
		if got := worstVerdict(c.a, c.b); got != c.want {
			t.Errorf("worstVerdict(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30}
	if got := percentile(sorted, 0.50); got != 20 {
		t.Errorf("p50 of %v = %d, want 20", sorted, got)
	}
	if got := percentile(sorted, 0.95); got != 30 {
		t.Errorf("p95 of %v = %d, want 30", sorted, got)
	}
	if got := percentile(nil, 0.50); got != 0 {
		t.Errorf("percentile of an empty slice = %d, want 0", got)
	}
}
