// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// NewWorkflowEngine builds the engine with the shipped starter set and
// the system invariants: the starters are catalog automations (instance-
// gated, pausable) — stage_change_create_task from automation, route_lead
// from people (the routing decision is transactional lead-store SQL) —
// while the lead-score recompute is a formula obligation
// (formulas-and-rules §3 — "recomputed on each captured signal") and
// fires always.
func NewWorkflowEngine(pool *pgxpool.Pool) *automation.WorkflowEngine {
	engine := automation.NewWorkflowEngine(pool)
	peopleStore := people.NewStore(pool)
	for _, handler := range automation.StarterWorkflows(NewProvider(pool)) {
		engine.RegisterWorkflow(handler)
	}
	engine.RegisterWorkflow(people.LeadRoutingWorkflow(peopleStore))
	for _, handler := range people.LeadScoreWorkflows(peopleStore) {
		engine.RegisterSystemWorkflow(handler)
	}
	return engine
}
