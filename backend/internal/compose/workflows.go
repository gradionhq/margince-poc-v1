// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	stager := automationApprovalsAdapter{svc: approvals.NewService(pool)}
	for _, handler := range automation.StarterWorkflows(NewProvider(pool), stager) {
		engine.RegisterWorkflow(handler)
	}
	engine.RegisterWorkflow(people.LeadRoutingWorkflow(peopleStore))
	for _, handler := range people.LeadScoreWorkflows(peopleStore) {
		engine.RegisterSystemWorkflow(handler)
	}
	return engine
}

// automationApprovalsAdapter maps the automation module's staging seam
// (automation.Approvals) onto the approvals module — the same cross-
// module edge approvalsAdapter (registry.go) wires for the MCP tool
// surface, but automation.StageRequest is its own type (a module cannot
// import a sibling's request shape, ADR-0054 §9), so it needs its own
// adapter rather than reusing that one.
type automationApprovalsAdapter struct{ svc *approvals.Service }

func (a automationApprovalsAdapter) Stage(ctx context.Context, in automation.StageRequest) (ids.ApprovalID, error) {
	return a.svc.Stage(ctx, approvals.StageInput{
		Kind:           in.Kind,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
	})
}
