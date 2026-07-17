// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The both-fields registration guard (Task 14a): RegisterWorkflow already
// rejected a handler declaring NEITHER EventType nor Schedule
// (engine.go). Now that real clock handlers (Schedule-bearing) sit
// beside the event handlers (EventType-bearing), a handler that wrongly
// set BOTH would have its non-matches silently swallowed as a clock
// trigger's (runOne never records a clock non-match, engine_run.go)
// even though it also claims to ride the bus as an event trigger. This
// file converts isClockTrigger's documented "exactly one, never both"
// convention into an enforced registration-time guard, proven two ways:
// RegisterWorkflow itself refuses a synthetic both-fields spec, and every
// REAL shipped handler is swept for the same defect.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// bothFieldsHandler is a synthetic handler whose Spec sets BOTH trigger
// fields — never a shipped shape, just the fixture RegisterWorkflow must
// refuse. Every method beyond Spec panics: a registration test never
// reaches Match/Plan/Apply/IdempotencyKey.
type bothFieldsHandler struct{}

func (bothFieldsHandler) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "both_fields_test_handler",
		Trigger: workflow.Trigger{EventType: "some.event", Schedule: "@daily"},
		Tier:    mcp.TierGreen,
	}
}

func (bothFieldsHandler) Match(context.Context, workflow.Event) (bool, error) {
	panic("bothFieldsHandler: Match not stubbed — a registration test never reaches dispatch")
}

func (bothFieldsHandler) Plan(context.Context, workflow.Event) (workflow.Effect, error) {
	panic("bothFieldsHandler: Plan not stubbed — a registration test never reaches dispatch")
}

func (bothFieldsHandler) Apply(context.Context, workflow.Event, workflow.Effect, *workflow.ApprovalToken) (workflow.RunResult, error) {
	panic("bothFieldsHandler: Apply not stubbed — a registration test never reaches dispatch")
}

func (bothFieldsHandler) IdempotencyKey(workflow.Event) string {
	panic("bothFieldsHandler: IdempotencyKey not stubbed — a registration test never reaches dispatch")
}

// mustPanic asserts fn panics; used here (rather than a bare recover in
// each test) because RegisterWorkflow's own guards are composition-time
// assertions (//craft:ignore panic-in-domain, engine.go) proven by
// deliberately triggering them, not by asserting a returned error.
func mustPanic(t *testing.T, why string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("no panic: %s", why)
		}
	}()
	fn()
}

func TestRegisterWorkflowRejectsBothTriggerFields(t *testing.T) {
	engine := NewWorkflowEngine(nil, nil)
	mustPanic(t, "a handler declaring both an EventType and a Schedule must be refused at registration", func() {
		engine.RegisterWorkflow(bothFieldsHandler{})
	})
}

// TestNoStarterWorkflowDeclaresBothTriggerFields is the fitness function:
// the handler list is derived from the shipped starter library (rather
// than maintained as a parallel list of names), so a future starter that
// slips in a both-fields Spec fails here even before it ever reaches
// RegisterWorkflow in a real composition.
func TestNoStarterWorkflowDeclaresBothTriggerFields(t *testing.T) {
	for _, h := range StarterWorkflows(Executors{}) {
		trigger := h.Spec().Trigger
		if trigger.EventType != "" && trigger.Schedule != "" {
			t.Errorf("%s declares BOTH an event type (%q) and a schedule (%q) — a handler must pick exactly one trigger shape",
				h.Spec().Name, trigger.EventType, trigger.Schedule)
		}
		if trigger.EventType == "" && trigger.Schedule == "" {
			t.Errorf("%s declares neither an event type nor a schedule", h.Spec().Name)
		}
	}
}
