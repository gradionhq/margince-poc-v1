// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The shipped corpus's own self-test: no e2e_llm build tag, no network, no
// model call — LoadCorpus is a pure parse over the committed corpus/ tree
// (aicert.LoadCorpus's own doc: "no time.Now, no network, no database").
// This is what keeps "every AI task has at least one certifiable scenario"
// an enforced invariant rather than a one-time authoring claim: a task
// added to ai-tasks.yaml (and so to ai.AllTasks()) without a matching
// corpus/<task>/ scenario fails this test, the same way arch_test.go's
// fitness tests derive their obligations from the tree rather than a
// maintained list.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestLoadCorpusCoversEveryTask(t *testing.T) {
	scenarios, err := aicert.LoadCorpus("corpus")
	if err != nil {
		t.Fatalf("LoadCorpus(corpus): %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("the shipped corpus loaded zero scenarios")
	}

	seen := map[ai.Task]int{}
	for _, sc := range scenarios {
		seen[ai.Task(sc.Task)]++
	}

	var missing []ai.Task
	for _, task := range ai.AllTasks() {
		if seen[task] == 0 {
			missing = append(missing, task)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("tasks with no corpus scenario: %v (every ai.AllTasks() entry must have >= 1 scenario under corpus/<task>/)", missing)
	}
}
