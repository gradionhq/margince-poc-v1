// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every ai_task an emitter writes into the AI-activity projection must be a
// task the AI contract declares.
//
// It lives at the ROOT because a module may not import the ai module to check
// itself: activities owns document readings, agents owns scheduled runs, ai
// owns the task vocabulary, and a module never imports a sibling.
//
// What it catches is narrow and real: ai_task_run.ai_task is a free-text column
// the projection copies straight out of the event, so an emitter that writes a
// task name the contract does not have produces a row nothing can join to a
// cost, a routing decision or a certification record — and nothing at runtime
// says a word about it.
//
// BOTH sides are derived. The emitted side used to be a hand-written map, and
// it held one entry while two emitters existed — the runner's task had been
// live for a release and this gate had never seen it. A list of who emits is
// the same thing that goes stale as a list of who should, so the emitters here
// are read from their own exported constants and the registry that routes them.

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestEveryEmittedAITaskIsDeclaredInTheContract(t *testing.T) {
	declared := make([]string, 0, len(ai.AllTasks()))
	for _, task := range ai.AllTasks() {
		declared = append(declared, string(task))
	}
	if len(declared) == 0 {
		t.Fatal("derived no tasks from the generated contract table; this gate would pass vacuously")
	}

	// The carriers name their task in their own exported constant; the router
	// announces under the task's own name, so the registry IS its emitted set.
	emitted := map[string]string{
		"activities.ExtractionAITask": activities.ExtractionAITask,
		"runner.ActivityAITask":       runner.ActivityAITask,
	}
	for task, source := range ai.RailOwners() {
		if source == ai.SourceRouter {
			emitted["the router, on behalf of "+task] = task
		}
	}
	for name, task := range emitted {
		if !slices.Contains(declared, task) {
			t.Errorf("%s writes ai_task %q, which api/ai-tasks.yaml does not declare — the projection would hold a task name nothing can join to a cost, a routing decision or a certification record", name, task)
		}
	}
}
