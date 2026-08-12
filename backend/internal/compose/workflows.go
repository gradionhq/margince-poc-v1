// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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
func NewWorkflowEngine(db *database.DB) *automation.WorkflowEngine {
	return workflowEngineWithDrafter(db, nil)
}

// NewWorkflowEngineWithReplyDraft adds the routed reply lane to draft_email
// actions while preserving NewWorkflowEngine's deterministic default.
func NewWorkflowEngineWithReplyDraft(db *database.DB, brain completer) *automation.WorkflowEngine {
	if brain == nil {
		return NewWorkflowEngine(db)
	}
	drafter := newReplyDrafter(db.Pool(), brain, nil)
	return workflowEngineWithDrafter(db, drafter)
}

func workflowEngineWithDrafter(db *database.DB, drafter activities.EmailDrafter) *automation.WorkflowEngine {
	// identity.Service implements shared/ports/authz.Resolver — the
	// match-time owner-permission gate's (gate.go) authority source. The
	// engine depends only on the port; this is the one place a concrete
	// identity is injected (ADR-0054 §8), same as platform/auth.NewGate.
	engine := automation.NewWorkflowEngine(db, identity.NewService(db.Pool()))
	peopleStore := people.NewStore(db)
	// Executors ride the same per-workspace dispatch as every other
	// datasource consumer: a starter firing for an overlay-mode
	// workspace reads/writes through the overlay seam, not silently
	// against the native tables that workspace no longer owns. The overlay
	// provider here carries no live-incumbent resolver (the nil below), so
	// a starter never triggers a force-fresh spend; its OVB meter is a
	// fail-closed placeholder (no Redis), never charged.
	ex := automation.Executors{
		Provider:  NewDispatcher(NewProvider(db.Pool()), NewOverlayProvider(db.Pool(), failClosedOverlayMeter(), nil), db.Pool()),
		Approvals: automationApprovalsAdapter{svc: approvals.NewService(db)},
		Lists:     listsAdapter{store: collections.NewStore(db)},
		// The zero SendPath is the honest one here: a send_email action
		// stages an approval instead of sending, and automation.Comms
		// declares DraftEmail alone, so no send is reachable from this
		// surface to configure.
		Comms: newCommsAdapter(db.Pool(), drafter, SendPath{}),
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
