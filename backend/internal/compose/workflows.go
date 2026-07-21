// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// NewWorkflowEngine builds the engine with the shipped starter set and
// the system invariants: the starters are catalog automations (instance-
// gated, pausable) — the automation module's own seven handlers
// (StarterWorkflows, incl. route_lead's create_task reading) plus
// assign_lead_owner from people (the routing decision is transactional
// lead-store SQL — AUTO-NOTE-2, §3.5: assign_lead_owner ASSIGNS AN
// OWNER, a different act from automation's own route_lead, which
// creates a task) — while the lead-score recompute is a formula
// obligation (formulas-and-rules §3 — "recomputed on each captured
// signal") and fires always.
func NewWorkflowEngine(pool *pgxpool.Pool) *automation.WorkflowEngine {
	return workflowEngineWithDrafter(pool, nil)
}

// NewWorkflowEngineWithReplyDraft adds the routed reply lane to draft_email
// actions while preserving NewWorkflowEngine's deterministic default.
func NewWorkflowEngineWithReplyDraft(pool *pgxpool.Pool, brain completer) *automation.WorkflowEngine {
	if brain == nil {
		return NewWorkflowEngine(pool)
	}
	drafter := newReplyDrafter(pool, brain, nil)
	return workflowEngineWithDrafter(pool, drafter)
}

func workflowEngineWithDrafter(pool *pgxpool.Pool, drafter activities.EmailDrafter) *automation.WorkflowEngine {
	// identity.Service implements shared/ports/authz.Resolver — the
	// match-time owner-permission gate's (gate.go) authority source. The
	// engine depends only on the port; this is the one place a concrete
	// identity is injected (ADR-0054 §8), same as platform/auth.NewGate.
	engine := automation.NewWorkflowEngine(pool, identity.NewService(pool))
	peopleStore := people.NewStore(pool)
	// Executors ride the same per-workspace dispatch as every other
	// datasource consumer: a starter firing for an overlay-mode
	// workspace reads/writes through the overlay seam, not silently
	// against the native tables that workspace no longer owns. This
	// engine's own OVB meter is independent of the REST surface's
	// (NewOverlayMeter's doc note).
	ex := automation.Executors{
		Provider:  NewDispatcher(NewProvider(pool), NewOverlayProvider(pool, NewOverlayMeter(), nil), pool),
		Approvals: automationApprovalsAdapter{svc: approvals.NewService(pool)},
		Lists:     listsAdapter{store: collections.NewStore(pool)},
		Comms: commsAdapter{
			store: activities.NewStore(pool),
			gate:  consent.NewGate(consent.NewStore(pool)),
			draft: drafter,
		},
		// Notifier stays nil: this repo wires no notification transport
		// (no notification table, the inbox is approvals-only) — a
		// notify firing surfaces as a visible 'skipped' run instead
		// (automation.ErrNoNotificationTransport, engine_run.go) until a
		// real channel lands here.
	}
	for _, handler := range automation.StarterWorkflows(ex) {
		engine.RegisterWorkflow(handler)
	}
	engine.RegisterWorkflow(people.LeadRoutingWorkflow(peopleStore))
	for _, handler := range people.LeadScoreWorkflows(peopleStore) {
		engine.RegisterSystemWorkflow(handler)
	}
	return engine
}

// listsAdapter maps automation.Lists onto collections.Store.AddMember,
// dropping the returned member row: an add_to_list action only needs to
// know whether the membership write succeeded.
type listsAdapter struct{ store *collections.Store }

var _ automation.Lists = listsAdapter{}

func (l listsAdapter) AddMember(ctx context.Context, listID ids.ListID, entityType string, entityID ids.UUID) error {
	_, err := l.store.AddMember(ctx, listID, entityType, entityID)
	return err
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
