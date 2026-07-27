// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The model path's lane names as a fitness function: a lane is named for the
// ai.Task it rides, so a reader goes from a field name straight to the routing
// ladder, the budget posture and the certification record that govern it. A
// name that has drifted from its task reads as one workload and bills as
// another, and only a reviewer's memory ever catches that. The allowed names
// are derived from the contract's shipped task set, so a task renamed upstream
// fails here instead of leaving a stale lane behind.
//
// The rule is one-directional on purpose: a shipped task with no lane is
// legitimate. cert_judge has none because the certification lane builds the
// judge its own router (aicert/runner.go) rather than the candidate's binding,
// so a candidate can never grade itself.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// laneExemptions are the ModelPath fields that are NOT task lanes, each with
// the reason it is not. An exemption without a reason is itself a finding: the
// point of the list is that adding to it costs an explanation.
var laneExemptions = map[string]string{
	"Embedder": "the retrieval embed lane — the router itself under no task label, typed search.Embedder",
}

// taskConstantName renders a contract task name the way tools/gen-aitasks
// renders the ai.Task… constant suffix (cert_judge -> CertJudge), so the
// allowed lane names ARE the generated identifiers rather than a second
// spelling of them.
func taskConstantName(task ai.Task) string {
	var b strings.Builder
	for _, part := range strings.Split(string(task), "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// exportedLanes returns the exported fields of ModelPath in declaration order.
func exportedLanes(t reflect.Type) []reflect.StructField {
	var lanes []reflect.StructField
	for i := range t.NumField() {
		if field := t.Field(i); field.IsExported() {
			lanes = append(lanes, field)
		}
	}
	return lanes
}

func TestEveryModelLaneIsNamedForAShippedTask(t *testing.T) {
	shipped := map[string]bool{}
	for _, task := range ai.AllTasks() {
		if ai.Status(task) == ai.StatusShipped {
			shipped[taskConstantName(task)] = true
		}
	}
	if len(shipped) == 0 {
		t.Fatal("the task contract declares no shipped task: the allowed lane names would be derived from nothing")
	}

	for _, field := range exportedLanes(reflect.TypeOf(ModelPath{})) {
		if reason, exempt := laneExemptions[field.Name]; exempt {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("ModelPath.%s is exempted from the lane-naming rule with no reason — state why it is not a task lane, or drop the exemption", field.Name)
			}
			continue
		}
		if !shipped[field.Name] {
			t.Errorf("ModelPath.%s: a lane is named for the ai.Task it serves, and %q is no shipped task's constant name — rename the field to that task's constant suffix (ai.TaskEnrich becomes Enrich), or add it to laneExemptions with the reason it is not a task lane", field.Name, field.Name)
		}
	}
}

// The name is only true if the wiring agrees with it. A lane the constructor
// never binds is a nil seam behind a name that promises a model, and a lane
// bound to a task other than its own bills and routes as a workload nobody
// reading the field name would expect — neither is visible at the call site.
func TestEveryModelLaneIsWiredToTheTaskItIsNamedFor(t *testing.T) {
	path, err := NewLocalModelPath(ai.FakeRoutingConfig())
	if err != nil {
		t.Fatalf("building the offline model path: %v", err)
	}
	value := reflect.ValueOf(path)
	for _, field := range exportedLanes(value.Type()) {
		lane := value.FieldByIndex(field.Index)
		if lane.Kind() == reflect.Interface && lane.IsNil() {
			t.Errorf("ModelPath.%s is declared but the model path constructor leaves it nil — bind it in modelPathForRouter so it rides the same router as every other lane", field.Name)
			continue
		}
		if _, exempt := laneExemptions[field.Name]; exempt {
			continue
		}
		var task ai.Task
		switch bound := lane.Interface().(type) {
		case routerBrain:
			task = bound.task
		case agentBrain:
			task = ai.TaskAgentLoop
		default:
			t.Errorf("ModelPath.%s is bound to a %T, which carries no task label — a lane rides the router under its own ai.Task, or it is not a lane", field.Name, bound)
			continue
		}
		if named := taskConstantName(task); named != field.Name {
			t.Errorf("ModelPath.%s rides task %q — rename the field to %s, or bind it to ai.Task%s", field.Name, task, named, field.Name)
		}
	}
}
