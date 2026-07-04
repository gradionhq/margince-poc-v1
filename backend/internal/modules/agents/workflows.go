// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

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
	pool     *pgxpool.Pool
}

func NewWorkflowEngine(pool *pgxpool.Pool) *WorkflowEngine {
	return &WorkflowEngine{pool: pool}
}

// RegisterWorkflow adds one handler at composition time.
func (e *WorkflowEngine) RegisterWorkflow(h workflow.Handler) {
	spec := h.Spec()
	if spec.Name == "" {
		panic("crmagents: registering a workflow with no name")
	}
	if spec.Trigger.EventType == "" && spec.Trigger.Schedule == "" {
		panic(fmt.Sprintf("crmagents: workflow %s declares no trigger", spec.Name))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.handlers {
		if existing.Spec().Name == spec.Name {
			panic(fmt.Sprintf("crmagents: duplicate workflow %s", spec.Name))
		}
	}
	e.handlers = append(e.handlers, h)
	sort.Slice(e.handlers, func(i, j int) bool { return e.handlers[i].Spec().Name < e.handlers[j].Spec().Name })
}

// HandleEvent is the cg:workflows consumer: every registered handler
// whose trigger names this event type gets its Match→Plan→Apply pass.
// Handler failures are isolated — one broken automation never starves
// its siblings — and land on the run row.
func (e *WorkflowEngine) HandleEvent(ctx context.Context, env kevents.Envelope) error {
	e.mu.RLock()
	handlers := append([]workflow.Handler(nil), e.handlers...)
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

	var firstErr error
	for _, h := range handlers {
		if h.Spec().Trigger.EventType != env.Type {
			continue
		}
		if err := e.runOne(runCtx, h, ev); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("workflow %s: %w", h.Spec().Name, err)
		}
	}
	return firstErr
}

func (e *WorkflowEngine) runOne(ctx context.Context, h workflow.Handler, ev workflow.Event) error {
	matched, err := h.Match(ctx, ev)
	if err != nil {
		return err
	}
	if !matched {
		return nil
	}
	effect, err := h.Plan(ctx, ev)
	if err != nil {
		return err
	}
	plannedJSON, err := json.Marshal(effect.Actions)
	if err != nil {
		return err
	}

	// Claim the (handler, key) row FIRST: whoever inserts runs; a
	// redelivery finds the claim and stops (AC-W3).
	claimed := false
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO workflow_run (workspace_id, handler, idempotency_key, trigger_event, planned, status)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, 'applied')
			ON CONFLICT (workspace_id, handler, idempotency_key) DO NOTHING`,
			h.Spec().Name, h.IdempotencyKey(ev), ev.ID, plannedJSON)
		if err != nil {
			return err
		}
		claimed = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	result, applyErr := h.Apply(ctx, ev, effect, nil)
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		switch {
		case errors.Is(applyErr, apperrors.ErrRequiresApproval):
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'requires_approval'
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, h.IdempotencyKey(ev))
			return err
		case applyErr != nil:
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'failed', error = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, h.IdempotencyKey(ev), applyErr.Error())
			if err != nil {
				return err
			}
			return applyErr
		default:
			appliedJSON, err := json.Marshal(result.Applied)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET applied = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, h.IdempotencyKey(ev), appliedJSON)
			return err
		}
	})
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
