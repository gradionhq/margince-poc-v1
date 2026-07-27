// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The shipped corpus's own self-test: no e2e_llm build tag, no network, no
// model call — LoadCorpus is a pure parse over the committed corpus/ tree
// (aicert.LoadCorpus's own doc: "no time.Now, no network, no database").
// The obligation follows the contract's status, in both directions: a
// shipped task without a corpus/<task>/ scenario has nothing to certify,
// and a planned task WITH one scores a prompt that does not ship — which
// reads as coverage it has not earned. Both are derived from
// ai-tasks.yaml rather than a maintained list, the same way arch_test.go's
// fitness tests derive their obligations from the tree.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestLoadCorpusCoversEveryTask(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := aicert.LoadCorpus("corpus", census)
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
	var unexpected []ai.Task
	for _, task := range ai.AllTasks() {
		switch ai.Status(task) {
		case ai.StatusShipped:
			if seen[task] == 0 {
				missing = append(missing, task)
			}
		case ai.StatusPlanned:
			if seen[task] > 0 {
				unexpected = append(unexpected, task)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("shipped tasks with no corpus scenario: %v", missing)
	}
	if len(unexpected) > 0 {
		t.Errorf("planned tasks carry corpus scenarios: %v — a task nobody built cannot be certified, and its scenario reads as coverage", unexpected)
	}
}
