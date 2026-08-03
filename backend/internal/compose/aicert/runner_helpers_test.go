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

// openRouterRoutingConfig is a base bound the way an operator binds
// OpenRouter: the generic OpenAI-wire provider, whose endpoint is a property
// of the vendor rather than of the model, so every tier carries the same
// base_url.
func openRouterRoutingConfig() ai.RoutingConfig {
	openRouter := ai.ProviderConfig{
		Provider: "openai_compatible",
		Model:    "mistralai/mistral-small-3.2-24b-instruct",
		BaseURL:  "https://openrouter.ai/api",
	}
	return ai.RoutingConfig{
		Profile: ai.ProfileCloudFrontier,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierLocalSmall: openRouter,
			ai.TierCheapCloud: openRouter,
			ai.TierPremium:    openRouter,
		},
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: openRouter, Dimensions: 1024},
	}
}

func TestOverrideForTaskKeepsTheEndpointWhenTheOverrideNamesTheSameProvider(t *testing.T) {
	base := openRouterRoutingConfig()

	overridden, err := overrideForTask(base, ai.TaskColdStart, "openai_compatible:z-ai/glm-5.2")
	if err != nil {
		t.Fatalf("same-provider override rejected: %v", err)
	}
	for _, tier := range ai.TaskLadder(ai.TaskColdStart) {
		binding := overridden.Tiers[tier]
		if binding.Model != "z-ai/glm-5.2" {
			t.Errorf("tier %s model = %q, want the override's model", tier, binding.Model)
		}
		// SelectBrain fails closed on openai_compatible without a base_url,
		// so an override that drops it cannot be run at all.
		if binding.BaseURL != "https://openrouter.ai/api" {
			t.Errorf("tier %s base_url = %q, want the base binding's endpoint", tier, binding.BaseURL)
		}
	}
}

func TestOverrideForTaskDropsTheEndpointWhenTheOverrideSwitchesProvider(t *testing.T) {
	base := openRouterRoutingConfig()

	overridden, err := overrideForTask(base, ai.TaskColdStart, "gemini:gemini-3.5-flash")
	if err != nil {
		t.Fatalf("cross-provider override rejected: %v", err)
	}
	for _, tier := range ai.TaskLadder(ai.TaskColdStart) {
		binding := overridden.Tiers[tier]
		if binding.Provider != "gemini" {
			t.Errorf("tier %s provider = %q, want the override's provider", tier, binding.Provider)
		}
		if binding.BaseURL != "" {
			t.Errorf("tier %s kept base_url %q across a provider switch; one vendor's host root addresses no other", tier, binding.BaseURL)
		}
	}
}

func TestOverrideForTaskKeepsAVariantSuffixedModelSlugWhole(t *testing.T) {
	base := openRouterRoutingConfig()

	// OpenRouter marks a model's served variant with a colon suffix
	// (":free", ":batch", ":thinking"), so the provider/model split must cut
	// at the FIRST colon and leave the rest of the slug alone.
	overridden, err := overrideForTask(base, ai.TaskColdStart, "openai_compatible:openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("variant-suffixed override rejected: %v", err)
	}
	for _, tier := range ai.TaskLadder(ai.TaskColdStart) {
		if got := overridden.Tiers[tier].Model; got != "openai/gpt-oss-20b:free" {
			t.Errorf("tier %s model = %q, want the whole slug including its variant suffix", tier, got)
		}
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
