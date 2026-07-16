// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The deterministic automation path (interfaces.md §5): typed handlers
// in a registry, driven off the bus — the predictable sibling of the
// model-driven runner. Both live in this module and act through the
// same governed machinery; a handler's Effect is a closed set of typed
// actions, never free-form side effects. Runs claim a
// (handler, idempotency-key) row first, so at-least-once delivery
// applies each effect exactly once.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// WorkflowEngine dispatches bus events to registered workflow.Handlers:
// Match → Plan → claim → Apply, with the run row as both idempotency
// claim and replayable record.
type WorkflowEngine struct {
	mu       sync.RWMutex
	handlers []workflow.Handler
	// system handlers are formula/invariant executors (lead-score
	// recompute): always on, never instance-gated — they are not user
	// automations, so the catalog and the paused/enabled surface do not
	// apply to them.
	system []workflow.Handler
	pool   *pgxpool.Pool
}

func NewWorkflowEngine(pool *pgxpool.Pool) *WorkflowEngine {
	return &WorkflowEngine{pool: pool}
}

// RegisterWorkflow adds one handler at composition time.
func (e *WorkflowEngine) RegisterWorkflow(h workflow.Handler) {
	spec := h.Spec()
	if spec.Name == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: registering a workflow with no name")
	}
	if spec.Trigger.EventType == "" && spec.Trigger.Schedule == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: workflow %s declares no trigger", spec.Name))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.handlers {
		if existing.Spec().Name == spec.Name {
			//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
			panic(fmt.Sprintf("crmagents: duplicate workflow %s", spec.Name))
		}
	}
	e.handlers = append(e.handlers, h)
	sort.Slice(e.handlers, func(i, j int) bool { return e.handlers[i].Spec().Name < e.handlers[j].Spec().Name })
}

// RegisterSystemWorkflow adds an always-on invariant handler: it fires
// on every matching event with no automation instance behind it. The
// run row still claims (handler, key), so redelivery stays exactly-once.
func (e *WorkflowEngine) RegisterSystemWorkflow(h workflow.Handler) {
	spec := h.Spec()
	if spec.Name == "" || spec.Trigger.EventType == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: system workflow needs a name and an event trigger")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.system = append(e.system, h)
	sort.Slice(e.system, func(i, j int) bool { return e.system[i].Spec().Name < e.system[j].Spec().Name })
}

// HandleEvent is the cg:workflows consumer: every registered handler
// whose trigger names this event type runs once per ENABLED automation
// instance of its type in the event's workspace (B-E15.4) — a paused,
// archived, or never-configured instance means the handler does not
// fire, and the instance's params ride the event into Plan. Handler
// failures are isolated — one broken automation never starves its
// siblings — and land on the run row.
func (e *WorkflowEngine) HandleEvent(ctx context.Context, env kevents.Envelope) error {
	// The engine consumes its own staging outcomes on the same group: a
	// rejected approval flips the parked run to 'blocked' (A72 honest
	// run history). No workflow triggers on approval.decided, so this is
	// the event's only engine-side effect.
	if env.Type == "approval.decided" {
		return e.HandleApprovalDecided(ctx, env)
	}
	e.mu.RLock()
	handlers := append([]workflow.Handler(nil), e.handlers...)
	system := append([]workflow.Handler(nil), e.system...)
	e.mu.RUnlock()

	ev := workflow.Event{
		ID:          env.EventID,
		Type:        env.Type,
		WorkspaceID: env.WorkspaceID,
		OccurredAt:  env.OccurredAt,
		Entity:      datasource.EntityRef{Type: datasource.EntityType(env.Entity.Type), ID: env.Entity.ID},
		Payload:     env.Payload,
	}
	// Workflows are deterministic system automations; their writes are
	// attributed to the system actor and grouped per trigger event.
	runCtx := principal.WithWorkspaceID(ctx, env.WorkspaceID)
	runCtx = principal.WithActor(runCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	runCtx = principal.WithCorrelationID(runCtx, ids.NewV7())
	runCtx = principal.WithCausationEvent(runCtx, env.EventID)

	instances, err := e.liveInstances(runCtx)
	if err != nil {
		return fmt.Errorf("loading automation instances: %w", err)
	}

	var firstErr error
	for _, h := range system {
		if h.Spec().Trigger.EventType != env.Type {
			continue
		}
		if err := e.runOne(runCtx, h, ev); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("workflow %s: %w", h.Spec().Name, err)
		}
	}
	for _, h := range handlers {
		if h.Spec().Trigger.EventType != env.Type {
			continue
		}
		for _, inst := range instances[h.Spec().Name] {
			iev := ev
			iev.AutomationID = inst.id.UUID
			iev.Params = inst.params
			if err := e.runOne(runCtx, h, iev); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("workflow %s: %w", h.Spec().Name, err)
			}
		}
	}
	return firstErr
}

// automationInstance is the enabled-row slice dispatch needs.
type automationInstance struct {
	id     ids.AutomationID
	params json.RawMessage
}

// liveInstances loads the workspace's enabled, unarchived automations,
// keyed by catalog key (== handler name). Read fresh per event: pausing
// binds on the very next dispatch, no cache to invalidate.
func (e *WorkflowEngine) liveInstances(ctx context.Context) (map[string][]automationInstance, error) {
	out := map[string][]automationInstance{}
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, key, params FROM automation WHERE enabled AND archived_at IS NULL ORDER BY created_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var inst automationInstance
			var key string
			if err := rows.Scan(&inst.id, &key, &inst.params); err != nil {
				return err
			}
			out[key] = append(out[key], inst)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// runKey scopes the idempotency claim to the automation instance: two
// instances of one type each apply once per event, and a replay of
// either finds its own claim.
func runKey(h workflow.Handler, ev workflow.Event) string {
	return h.IdempotencyKey(ev) + "@" + ev.AutomationID.String()
}

// runOne makes EVERY firing outcome durable (B-E15.3a): matched-and-applied,
// skipped, failed, and staged-for-approval all land on the run row, each
// terminal reason on the `error` column — a run history that only shows
// successes hides exactly the runs a human needs to see.
func (e *WorkflowEngine) runOne(ctx context.Context, h workflow.Handler, ev workflow.Event) error {
	matched, err := h.Match(ctx, ev)
	if err != nil {
		return e.recordFailedBeforeApply(ctx, h, ev, "matching the trigger: "+err.Error(), err)
	}
	if !matched {
		return e.recordSkip(ctx, h, ev)
	}
	effect, err := h.Plan(ctx, ev)
	if err != nil {
		return e.recordFailedBeforeApply(ctx, h, ev, "planning the effect: "+err.Error(), err)
	}
	plannedJSON, err := json.Marshal(effect.Actions)
	if err != nil {
		return e.recordFailedBeforeApply(ctx, h, ev, "encoding the planned actions: "+err.Error(), err)
	}

	claimed, err := e.claimRun(ctx, h, ev, plannedJSON, "applied", nil)
	if err != nil || !claimed {
		return err
	}

	result, applyErr := h.Apply(ctx, ev, effect, nil)
	// The outcome record commits in its OWN transaction before the apply
	// error surfaces — returning applyErr from inside the tx closure would
	// roll the very 'failed' row back and leave the claim lying 'applied'.
	recordErr := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var staged *workflow.StagedApprovalError
		switch {
		case errors.As(applyErr, &staged):
			// The staging pointer rides the detail column — the run row's
			// only free seam — so a later rejection can find and block
			// exactly this run (workflows_blocked.go).
			_, err := tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'requires_approval', error = $3
				WHERE handler = $1 AND idempotency_key = $2`,
				h.Spec().Name, runKey(h, ev), stagedApprovalDetail(staged.ApprovalID))
			return err
		case errors.Is(applyErr, apperrors.ErrRequiresApproval):
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'requires_approval'
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, runKey(h, ev))
			return err
		case applyErr != nil:
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'failed', error = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, runKey(h, ev), applyErr.Error())
			return err
		default:
			appliedJSON, err := json.Marshal(result.Applied)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET applied = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, runKey(h, ev), appliedJSON)
			return err
		}
	})
	if recordErr != nil {
		return errors.Join(applyErr, recordErr)
	}
	if applyErr != nil && !errors.Is(applyErr, apperrors.ErrRequiresApproval) {
		// A staged 🟡 is a healthy suspension, not a dispatch failure; a
		// real apply failure still surfaces after its record committed.
		return applyErr
	}
	return nil
}

// claimRun claims the (handler, key) row FIRST: whoever inserts runs; a
// redelivery finds the claim and stops (AC-W3). Terminal-at-claim
// outcomes (skipped, failed before Apply) insert directly in their final
// state, so the claim doubles as the honest run record.
func (e *WorkflowEngine) claimRun(ctx context.Context, h workflow.Handler, ev workflow.Event, planned []byte, status string, detail *string) (bool, error) {
	claimed := false
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO workflow_run (workspace_id, handler, idempotency_key, trigger_event, planned, status, error)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (workspace_id, handler, idempotency_key) DO NOTHING`,
			h.Spec().Name, runKey(h, ev), ev.ID, planned, status, detail)
		if err != nil {
			return err
		}
		claimed = tag.RowsAffected() > 0
		return nil
	})
	return claimed, err
}

// emptyPlan is the planned column of a run that never reached Plan (a
// skip or a pre-Apply failure): no actions were ever on the table.
var emptyPlan = []byte(`[]`)

// recordFailedBeforeApply lands a Match/Plan/encode failure as a durable
// 'failed' run and still surfaces the cause to the dispatcher. The claim
// makes the failure terminal for this (handler, key) — the same
// no-retry-after-claim contract the Apply path already has.
func (e *WorkflowEngine) recordFailedBeforeApply(ctx context.Context, h workflow.Handler, ev workflow.Event, detail string, cause error) error {
	if _, claimErr := e.claimRun(ctx, h, ev, emptyPlan, "failed", &detail); claimErr != nil {
		return errors.Join(cause, claimErr)
	}
	return cause
}

// recordSkip lands a trigger-matched-but-conditions-declined outcome as a
// durable 'skipped' run (B-E15.3a): the designer's history shows the
// event arrived and why nothing happened, never a silent gap. System
// handlers are invariant executors with no automation instance behind
// them — no run history reads their skips, so recording them would only
// accrete rows.
func (e *WorkflowEngine) recordSkip(ctx context.Context, h workflow.Handler, ev workflow.Event) error {
	if ev.AutomationID == ids.Nil {
		return nil
	}
	detail := "the trigger event did not satisfy this automation's conditions"
	_, err := e.claimRun(ctx, h, ev, emptyPlan, "skipped", &detail)
	return err
}

// ApplyActions is the shared executor handlers delegate Apply to: each
// typed action runs through the SAME datasource seam every surface
// uses. The closed switch IS the anti-builder guard — an action kind
// the seam does not know is a programming error, not a plugin point.
func ApplyActions(ctx context.Context, provider datasource.SystemOfRecordProvider, effect workflow.Effect) ([]workflow.Action, error) {
	var applied []workflow.Action
	for _, action := range effect.Actions {
		switch action.Kind {
		case workflow.ActionCreateTask, workflow.ActionCreateRecord:
			entity := action.Target.Type
			if action.Kind == workflow.ActionCreateTask {
				entity = datasource.EntityActivity
			}
			if _, err := provider.Create(ctx, datasource.CreateInput{
				EntityType: entity,
				Fields:     action.Args,
				Source:     "system",
			}); err != nil {
				return applied, err
			}
		case workflow.ActionUpdateRecord, workflow.ActionAssignOwner:
			if _, err := provider.Update(ctx, datasource.UpdateInput{
				Ref:    action.Target,
				Patch:  action.Args,
				Source: "system",
			}); err != nil {
				return applied, err
			}
		case workflow.ActionAdvanceDeal, workflow.ActionSendEmail:
			// The 🟡 kinds: a deterministic handler carrying one of these
			// must arrive with an approval token; the token path lands
			// with the workflow-approval slice.
			return applied, fmt.Errorf("action %s: %w", action.Kind, apperrors.ErrRequiresApproval)
		case workflow.ActionEmitFlowEvent, workflow.ActionRecomputeScore, workflow.ActionEnqueueJob:
			// Declared kinds whose executors ride later slices; refusing
			// loudly beats silently claiming success.
			return applied, fmt.Errorf("crmagents: action %s has no executor yet", action.Kind)
		default:
			return applied, fmt.Errorf("crmagents: unknown action kind %q", action.Kind)
		}
		applied = append(applied, action)
	}
	return applied, nil
}
