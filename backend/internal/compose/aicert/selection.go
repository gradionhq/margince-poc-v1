// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// Task selection and per-task routing overrides: which scenarios a run
// certifies (corpus filter + deterministic order) and how a MODEL= override
// rebinds just the task-under-test's ladder without touching the judge's.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// overrideForTask rebinds ONLY the tiers on task's routing ladder to the
// MODEL= override's provider:model, over a COPY of base's tier map —
// base itself is never mutated, so the judge router built from the same
// base afterward still sees every tier as configured. An override naming
// the tier's own provider keeps that tier's base_url, since the endpoint
// addresses the vendor rather than the model; switching provider drops it.
// Empty override is a no-op: the candidate then rides base exactly like
// the judge.
func overrideForTask(base ai.RoutingConfig, task ai.Task, override string) (ai.RoutingConfig, error) {
	if override == "" {
		return base, nil
	}
	provider, modelName, found := strings.Cut(override, ":")
	if !found || provider == "" || modelName == "" {
		return ai.RoutingConfig{}, fmt.Errorf("aicert: MARGINCE_AICERT_MODEL wants provider:model, got %q", override)
	}
	ladder := ai.TaskLadder(task)
	if len(ladder) == 0 {
		return ai.RoutingConfig{}, fmt.Errorf("aicert: task %s has no routing ladder to override", task)
	}
	tiers := make(map[ai.Tier]ai.ProviderConfig, len(base.Tiers))
	for tier, binding := range base.Tiers {
		tiers[tier] = binding
	}
	for _, tier := range ladder {
		rebound := ai.ProviderConfig{Provider: provider, Model: modelName}
		// A same-provider override swaps the MODEL on a binding whose
		// endpoint belongs to the vendor, not to the model — and
		// openai_compatible fails closed without one, so dropping it would
		// make every OpenAI-wire candidate unrunnable. A cross-provider
		// override inherits nothing: one vendor's host root addresses no
		// other, and a tier base never bound has no endpoint to give
		// (provider is non-empty here, so a zero binding never matches).
		if base.Tiers[tier].Provider == provider {
			rebound.BaseURL = base.Tiers[tier].BaseURL
		}
		tiers[tier] = rebound
	}
	overridden := base
	overridden.Tiers = tiers
	return overridden, nil
}

// groupByTask buckets scenarios by their Task field, keeping only tasks
// matching filter when filter is non-empty.
func groupByTask(scenarios []Scenario, filter string) map[ai.Task][]Scenario {
	byTask := map[ai.Task][]Scenario{}
	for _, sc := range scenarios {
		if filter != "" && sc.Task != filter {
			continue
		}
		t := ai.Task(sc.Task)
		byTask[t] = append(byTask[t], sc)
	}
	return byTask
}

// sortedTasks returns byTask's keys in deterministic order, so two runs
// over the same corpus process tasks (and therefore emit any errors) in
// the same order.
func sortedTasks(byTask map[ai.Task][]Scenario) []ai.Task {
	tasks := make([]ai.Task, 0, len(byTask))
	for t := range byTask {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i] < tasks[j] })
	return tasks
}
