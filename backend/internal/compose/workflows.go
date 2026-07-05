// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// NewWorkflowEngine builds the engine with the shipped starter set and
// the system invariants: the starters are catalog automations (instance-
// gated, pausable); the lead-score recompute is a formula obligation
// (formulas-and-rules §3 — "recomputed on each captured signal") and
// fires always.
func NewWorkflowEngine(pool *pgxpool.Pool) *agents.WorkflowEngine {
	engine := agents.NewWorkflowEngine(pool)
	for _, handler := range agents.StarterWorkflows(NewProvider(pool)) {
		engine.RegisterWorkflow(handler)
	}
	for _, handler := range people.LeadScoreWorkflows(people.NewStore(pool)) {
		engine.RegisterSystemWorkflow(handler)
	}
	return engine
}
