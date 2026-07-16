// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package workflow defines the automation seam (interfaces.md §5,
// features/03 §5): workflows are typed handlers in a registry — code,
// agent-authored, test-guarded — not a visual builder. Each declares a
// trigger, a typed Effect, an idempotency key, and a risk tier; runs ride
// the job queue with retries, dead-letter, and audit.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Handler is the seam an agent implements to add automation. Registered
// by Spec().Name; subscribed by Spec().Trigger.
type Handler interface {
	Spec() Spec

	// Match is a pure predicate over the trigger event and related
	// records; false means the handler does not run.
	Match(ctx context.Context, ev Event) (bool, error)

	// Plan computes the typed Effect WITHOUT applying it — this is what
	// makes dry-run and diff preview possible. Deterministic given the
	// same event and DB snapshot.
	Plan(ctx context.Context, ev Event) (Effect, error)

	// Apply executes the planned Effect. 🟢 effects auto-execute; a 🟡
	// effect must carry an approval token or Apply returns
	// apperrors.ErrRequiresApproval. Idempotent on IdempotencyKey(ev).
	Apply(ctx context.Context, ev Event, eff Effect, token *ApprovalToken) (RunResult, error)

	// IdempotencyKey derives the stable key for this (handler, event) so
	// the queue and registry dedupe replays.
	IdempotencyKey(ev Event) string
}

type Spec struct {
	Name    string // stable id: "flag_idle_deals", "route_lead", …
	Trigger Trigger
	Tier    mcp.RiskTier
}

// Trigger binds to the event bus or a schedule: EventType for bus events,
// Schedule (cron) when EventType is empty.
type Trigger struct {
	EventType string
	Schedule  string
	Filter    map[string]any // cheap envelope pre-filter before Match
}

// Event is the bus envelope slice a handler sees (events.md §2), plus
// the automation instance driving this dispatch: the engine fires a
// handler once per enabled instance of its type, carrying that
// instance's validated params — the editor's parameterization reaches
// the run here.
type Event struct {
	ID          ids.UUID
	Type        string
	WorkspaceID ids.UUID
	OccurredAt  time.Time
	Entity      datasource.EntityRef
	Payload     json.RawMessage

	AutomationID ids.UUID
	Params       json.RawMessage
}

// Effect is the typed, enumerable set of actions a run may take. No
// free-form side effects: each action is a declared variant so dry-run,
// audit, and the 🟡 gate can reason about it.
type Effect struct {
	Actions []Action
}

// ActionKind enumerates the closed action set (features/03 §5.1); the
// closed-set contract test is the anti-builder guard.
type ActionKind string

const (
	ActionCreateRecord   ActionKind = "create_record"
	ActionUpdateRecord   ActionKind = "update_record"
	ActionCreateTask     ActionKind = "create_task"
	ActionAssignOwner    ActionKind = "assign_owner"
	ActionAdvanceDeal    ActionKind = "advance_deal"
	ActionSendEmail      ActionKind = "send_email"
	ActionEmitFlowEvent  ActionKind = "emit_flow_event"
	ActionRecomputeScore ActionKind = "recompute_score"
	ActionEnqueueJob     ActionKind = "enqueue_job"
)

// The user-facing catalog's actions that have no lower-level kind: notify
// is delivery to a human, add_to_list writes through the list engine, and
// draft_email creates a draft and never sends — the send is a separate,
// approval-gated act.
const (
	ActionNotify     ActionKind = "notify"
	ActionAddToList  ActionKind = "add_to_list"
	ActionDraftEmail ActionKind = "draft_email"
)

// AllActionKinds is the closed set, in declaration order. The registry maps
// the user-facing catalog onto these; a kind with no executor fails the
// totality test rather than reaching a caller.
func AllActionKinds() []ActionKind {
	return []ActionKind{
		ActionCreateRecord, ActionUpdateRecord, ActionCreateTask, ActionAssignOwner,
		ActionAdvanceDeal, ActionSendEmail, ActionEmitFlowEvent, ActionRecomputeScore,
		ActionEnqueueJob, ActionNotify, ActionAddToList, ActionDraftEmail,
	}
}

type Action struct {
	Kind   ActionKind
	Target datasource.EntityRef
	Args   json.RawMessage
}

// ApprovalToken references the typed, signed, single-use, effect-bound
// credential of ADR-0036; the approvals service owns its verification.
type ApprovalToken struct {
	Value string
}

// RunResult is audit-logged: a replayable trace of what was planned,
// approved, and applied.
type RunResult struct {
	RunID      ids.UUID
	Applied    []Action
	AuditLogID ids.UUID
}

// StagedApprovalError is the typed form of the "staged as approval"
// answer: a chat client shows the message, while a programmatic caller
// (the Surface-B runner) suspends on the id instead of parsing prose.
type StagedApprovalError struct{ ApprovalID ids.ApprovalID }

func (e *StagedApprovalError) Error() string {
	return fmt.Sprintf(
		"staged as approval %s — once a human approves it, repeat this exact call with \"approval_id\": %q: %s",
		e.ApprovalID, e.ApprovalID.String(), apperrors.ErrRequiresApproval)
}

func (e *StagedApprovalError) Unwrap() error { return apperrors.ErrRequiresApproval }
