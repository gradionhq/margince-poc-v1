// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every AI task this build can run reports into the AI-activity projection.
//
// The rail's claim is that it says what the AI is doing for one person. A task
// that reports nothing is AI work the product performed and then denied — and
// for seventeen of nineteen shipped tasks that was the state of the tree, with
// every gate green. The gates in place could not see it: they compared the
// contract's kind enum against the two producers already wired, which is a
// question whose answer can never be an absentee.
//
// So this gate asks the other question. It starts from the CONTRACT's task
// table — the one place a new AI task must be declared — and requires each task
// to name the source that reports it. Derived from the generated table rather
// than a list here, because a list is exactly what went stale to produce the
// defect.
//
// It lives at the root because no module can ask it: `ai` owns the task
// vocabulary, the carriers are its siblings, and a module never imports one.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// carrierSources is every non-router source in the registry, mapped to the
// constant the carrier really emits. The VALUE is what makes this more than a
// spelling check: it is read from the emitting package, so a carrier that
// renames or retires its source breaks the pairing instead of leaving the
// router silenced in favour of a source nothing writes.
var carrierSources = map[string]string{
	"agent_runner":          runner.ActivitySource,
	"attachment_extraction": activities.ExtractionActivitySource,
}

func TestEveryAITaskNamesTheSourceThatReportsIt(t *testing.T) {
	tasks := ai.AllTasks()
	if len(tasks) == 0 {
		t.Fatal("derived no tasks from the generated contract table; this gate would pass vacuously")
	}
	for _, task := range tasks {
		if ai.RailOwner(task) == "" {
			t.Errorf("task %q names no source that reports it, so its work never reaches ai_task_run and "+
				"the rail denies AI the product really ran. Answer it in ai.railOwners: ai.SourceRouter when "+
				"the router's post-hoc report is the whole truth, or a carrier's source when a durable row "+
				"can say queued and running", task)
		}
	}
}

// The reverse: a registry entry for a task the contract dropped is a silencing
// nobody can act on, and it would keep the router quiet about a name that no
// longer routes anywhere.
func TestTheRailRegistryNamesNoTaskTheContractDropped(t *testing.T) {
	declared := make(map[string]bool, len(ai.AllTasks()))
	for _, task := range ai.AllTasks() {
		declared[string(task)] = true
	}
	for task := range ai.RailOwners() {
		if !declared[task] {
			t.Errorf("the rail registry answers for task %q and api/ai-tasks.yaml does not declare it — "+
				"either the task was retired and its entry kept, or the entry is a typo that silently "+
				"leaves the real task unanswered", task)
		}
	}
}

// A carrier override silences the router for that task. If the source it names
// is one no module writes, the task is reported by NOBODY — which reads exactly
// like a task that is wired.
func TestEveryCarrierOverrideNamesASourceThatIsReallyEmitted(t *testing.T) {
	for task, source := range ai.RailOwners() {
		if source == ai.SourceRouter {
			continue
		}
		emitted, known := carrierSources[source]
		if !known {
			t.Errorf("task %q is reported by carrier source %q, and this gate knows no emitter for it — "+
				"the router is silenced for a task nothing announces. Add the carrier's exported source "+
				"constant to carrierSources, or point the task at ai.SourceRouter", task, source)
			continue
		}
		if emitted != source {
			t.Errorf("task %q is registered against source %q but its carrier emits %q — the projection "+
				"would hold the occurrence under a name the registry does not know", task, source, emitted)
		}
	}
}
