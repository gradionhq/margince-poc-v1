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
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
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

// systemSource is the provenance every deterministic workflow write
// stamps: an automation acts as the system, never on behalf of the
// human who authored or enabled it. Named once so ApplyActions'
// Create/Update calls and applyAssignOwner's (handlers_actions.go)
// cannot drift into three independent spellings of the same fact.
const systemSource = "system"

// ApplyActions is the shared executor handlers delegate Apply to: each
// typed action runs through the SAME set of seams every surface uses
// (ex, seams.go). The closed switch IS the anti-builder guard — an
// action kind the seam does not know is a programming error, not a
// plugin point. ex.Approvals is what a 🟡 action stages through — every
// caller holds one, even though a run never redeems mid-Apply today
// (runOne always calls Handler.Apply with a nil token).
func ApplyActions(ctx context.Context, ex Executors, effect workflow.Effect) ([]workflow.Action, error) {
	var applied []workflow.Action
	for _, action := range effect.Actions {
		recorded, staged, err := applyOne(ctx, ex, action)
		if err != nil {
			return applied, err
		}
		if staged != nil {
			// A 🟡 action stages rather than executing: the run parks
			// behind the approval id and nothing after it applies.
			return applied, staged
		}
		// recorded is the action AS APPLIED, which is not always the action
		// as planned: draft_email enriches it with the composed draft so the
		// run record durably holds the artifact (handlers_actions.go).
		applied = append(applied, recorded)
	}
	return applied, nil
}

// applyOne executes — or stages — one typed action through the seams and
// returns the action AS APPLIED (draft_email enriches it; every other kind
// returns it unchanged). The closed switch IS the anti-builder guard: a
// kind the seams do not know is a programming error, not a plugin point. A
// non-nil middle return is a 🟡 staging that short-circuits the batch.
func applyOne(ctx context.Context, ex Executors, action workflow.Action) (workflow.Action, *workflow.StagedApprovalError, error) {
	switch action.Kind {
	case workflow.ActionCreateTask, workflow.ActionCreateRecord:
		return action, nil, applyCreate(ctx, ex.Provider, action)
	case workflow.ActionUpdateRecord:
		return action, nil, applyUpdate(ctx, ex.Provider, action)
	case workflow.ActionAssignOwner:
		// AUTO-T07's dynamic tier: every real firing today is single-entity
		// (assign_owner_tier.go's AssignOwnerScope doc) — the zero-value
		// scope here is that honest default, not a fabricated bulk signal.
		// applyAssignOwner's own branch (🟢 write vs 🟡 stage) is proven
		// against a synthetic scaled scope by its unit tests.
		return action, nil, applyAssignOwner(ctx, ex, action, AssignOwnerScope{})
	case workflow.ActionAdvanceDeal, workflow.ActionSendEmail, workflow.ActionEmitFlowEvent:
		// The 🟡 kinds — request_approval's own executor is
		// ActionEmitFlowEvent, confirm-first by its very nature, same as
		// advance_deal-to-won/lost and send_email. A deterministic handler
		// carrying one of these stages the action for a human decision
		// instead of dead-ending: the run parks behind the resulting
		// approval id (runOne), and resuming a released staging is the token
		// path a later slice adds.
		id, err := stageForApproval(ctx, ex.Approvals, action)
		if err != nil {
			return action, nil, err
		}
		return action, &workflow.StagedApprovalError{ApprovalID: id}, nil
	case workflow.ActionNotify:
		return action, nil, applyNotify(ctx, ex.Notifier, action)
	case workflow.ActionAddToList:
		return action, nil, applyAddToList(ctx, ex.Lists, action)
	case workflow.ActionDraftEmail:
		recorded, err := applyDraftEmail(ctx, ex.Comms, action)
		return recorded, nil, err
	case workflow.ActionRecomputeScore, workflow.ActionEnqueueJob:
		// Declared kinds whose executors ride later slices; refusing loudly
		// beats silently claiming success.
		return action, nil, fmt.Errorf("crmagents: action %s has no executor yet", action.Kind)
	default:
		return action, nil, fmt.Errorf("crmagents: unknown action kind %q", action.Kind)
	}
}

func applyCreate(ctx context.Context, provider datasource.SystemOfRecordProvider, action workflow.Action) error {
	entity := action.Target.Type
	if action.Kind == workflow.ActionCreateTask {
		entity = datasource.EntityActivity
	}
	_, err := provider.Create(ctx, datasource.CreateInput{
		EntityType: entity,
		Fields:     action.Args,
		Source:     systemSource,
	})
	return err
}

func applyUpdate(ctx context.Context, provider datasource.SystemOfRecordProvider, action workflow.Action) error {
	_, err := provider.Update(ctx, datasource.UpdateInput{
		Ref:    action.Target,
		Patch:  action.Args,
		Source: systemSource,
	})
	return err
}

// stageForApproval builds the human-facing staging request for one 🟡
// action and hands it to the approvals seam. ProposedChange/DiffHash are
// derived the same way every other stager in this codebase derives them
// (e.g. compose/closedate.go): canonicalize the action's own args and
// hash that, never a fabricated placeholder — so a redelivered firing of
// the identical action reaches the identical diff_hash a human already
// saw, instead of minting a fresh unrecognizable staging each retry.
func stageForApproval(ctx context.Context, approvals Approvals, action workflow.Action) (ids.ApprovalID, error) {
	raw := action.Args
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	canonical, diffHash, err := diffhash.Canonical(raw)
	if err != nil {
		return ids.ApprovalID{}, fmt.Errorf("automation: canonicalizing %s for staging: %w", action.Kind, err)
	}
	return approvals.Stage(ctx, StageRequest{
		Kind:           string(action.Kind),
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     string(action.Target.Type),
		TargetID:       action.Target.ID,
		Summary:        fmt.Sprintf("automation wants to %s on %s %s", action.Kind, action.Target.Type, action.Target.ID),
	})
}
