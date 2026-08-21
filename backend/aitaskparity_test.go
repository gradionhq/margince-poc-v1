// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every ai_task an emitter writes into the AI-activity projection must be a
// task the AI contract declares.
//
// It lives at the ROOT because a module may not import the ai module to check
// itself: activities owns document readings, ai owns the task vocabulary, and
// a module never imports a sibling. The declared side is DERIVED from the
// generated table rather than listed here — a list would need maintaining, and
// the maintenance is the thing that gets forgotten.
//
// What it catches is narrow and real: ai_task_run.ai_task is a free-text column
// the projection copies straight out of the event, so an emitter that writes a
// task name the contract does not have produces a row nothing can join to a
// cost, a routing decision or a certification record — and nothing at runtime
// says a word about it.

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
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

	// One entry per emitter of ai_task into the projection. A new source adds
	// its constant here in the same change that starts emitting it.
	emitted := map[string]string{
		"activities.ExtractionAITask": activities.ExtractionAITask,
	}
	for name, task := range emitted {
		if !slices.Contains(declared, task) {
			t.Errorf("%s writes ai_task %q, which api/ai-tasks.yaml does not declare — the projection would hold a task name nothing can join to a cost, a routing decision or a certification record", name, task)
		}
	}
}
