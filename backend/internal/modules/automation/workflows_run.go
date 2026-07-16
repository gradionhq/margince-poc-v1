// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The per-firing run lifecycle WorkflowEngine.HandleEvent drives every
// dispatched handler through (workflows.go): claim the (handler,
// idempotency-key) row first, then Plan → Apply, recording every
// terminal outcome — applied, skipped, failed, staged — durably on the
// run row (B-E15.3a). Split out of workflows.go once the file neared the
// 500-line cap; no behavior changed by the move itself.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// runKey scopes the idempotency claim to the automation instance: two
// instances of one type each apply once per event, and a replay of
// either finds its own claim.
//
// For a CLOCK trigger (Trigger.Schedule set) the handler's
// IdempotencyKey must derive this from the ANCHOR that makes the
// condition true (last_activity_at, a due date, …), never from ev.ID —
// a clock condition is continuously true once its anchor is stale
// enough, so the key has to be stable across every re-evaluation of the
// SAME anchor and only change when the anchor itself moves. An
// event-trigger key carries ev.ID (or content derived from the event),
// which is exactly why an event trigger's non-match is safe to record
// (recordSkip below) and a clock trigger's is not (runOne's !matched
// branch).
func runKey(h workflow.Handler, ev workflow.Event) string {
	return h.IdempotencyKey(ev) + "@" + ev.AutomationID.String()
}

// isClockTrigger distinguishes the two trigger shapes runOne must treat
// differently: a Schedule-bearing handler is continuously re-evaluated
// against an anchor (its condition can flip false→true→false as the
// anchor moves), while an EventType-bearing handler fires once per
// discrete bus delivery. workflow.Trigger documents the two fields as
// mutually exclusive (EventType for bus events, Schedule when EventType
// is empty), and RegisterWorkflow/RegisterSystemWorkflow already reject
// a handler that sets neither — every handler this engine runs today
// sets exactly one, so Schedule != "" is a sound split in practice.
func isClockTrigger(h workflow.Handler) bool {
	return h.Spec().Trigger.Schedule != ""
}

// runOne makes EVERY firing outcome durable (B-E15.3a): matched-and-applied,
// skipped, failed, and staged-for-approval all land on the run row, each
// terminal reason on the `detail` column (rundetail.go) — a run history
// that only shows successes hides exactly the runs a human needs to see.
func (e *WorkflowEngine) runOne(ctx context.Context, h workflow.Handler, ev workflow.Event) error {
	matched, err := h.Match(ctx, ev)
	if err != nil {
		return e.recordFailedBeforeApply(ctx, h, ev, "matching the trigger: "+err.Error(), err)
	}
	if !matched {
		if isClockTrigger(h) {
			// A clock trigger's key is the anchor that makes its condition
			// true, so the key already exists while the condition is still
			// false. Recording a non-match here would claim that anchor key
			// (ON CONFLICT DO NOTHING), and the real firing when the anchor
			// finally matches would find the row taken and silently never run.
			// A coarse-pre-filter reject is pre-filter noise, not a
			// user-meaningful skip — do not persist it.
			return nil
		}
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

	// The match-time owner-permission gate (gate.go, AUTO-T06): a human-
	// authored firing (ev.OwnerID set) re-checks the OWNER's live RBAC
	// against the planned effect before Apply ever runs, closing the gap
	// left by the author-time ceiling (ceiling.go) — an automation's
	// author can lose their authority long after authoring it. runOne is
	// shared by the matcher (HandleEvent) and the time-scan, so wiring the
	// gate here gives both entries the one path, one gate.
	decision, err := checkOwnerPermission(ctx, e.resolver, ev, effect)
	if err != nil {
		// A transient resolver failure (DB down, …) is not a permission
		// answer: surface it so the firing retries later rather than
		// claiming a terminal run row over an infrastructure blip.
		return err
	}
	if decision.blocked {
		return e.recordBlocked(ctx, h, ev, plannedJSON, decision.reason)
	}

	claimed, err := e.claimRun(ctx, h, ev, plannedJSON, "applied", nil)
	if err != nil || !claimed {
		return err
	}

	result, applyErr := h.Apply(ctx, ev, effect, nil)
	// The outcome record commits in its OWN transaction before the apply
	// error surfaces — returning applyErr from inside the tx closure would
	// roll the very 'failed' row back and leave the claim lying 'applied'.
	if recordErr := e.recordApplyOutcome(ctx, h, ev, result, applyErr); recordErr != nil {
		return errors.Join(applyErr, recordErr)
	}
	if applyErr != nil && !errors.Is(applyErr, apperrors.ErrRequiresApproval) && !errors.Is(applyErr, ErrNoNotificationTransport) {
		// A staged 🟡 is a healthy suspension and a no-transport notify is
		// an honest out-of-scope skip — neither is a dispatch failure; a
		// real apply failure still surfaces after its record committed.
		return applyErr
	}
	return nil
}

// recordApplyOutcome writes the terminal shape of one Apply call onto its
// run row: which of the four outcomes (staged, no-transport skip, failed,
// applied) it was, plus the reason a human reads on the run (rundetail.go).
// Split out of runOne so the dispatch flow above stays readable as the
// outcome vocabulary grows.
func (e *WorkflowEngine) recordApplyOutcome(ctx context.Context, h workflow.Handler, ev workflow.Event, result workflow.RunResult, applyErr error) error {
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var staged *workflow.StagedApprovalError
		switch {
		case errors.As(applyErr, &staged):
			// The staging pointer rides the detail column's approval_id
			// field — the run row's only free seam — so a later rejection
			// can find and block exactly this run (workflows_blocked.go).
			detail, err := stagedApprovalDetail(staged.ApprovalID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'requires_approval', detail = $3
				WHERE handler = $1 AND idempotency_key = $2`,
				h.Spec().Name, runKey(h, ev), detail)
			return err
		case errors.Is(applyErr, ErrNoNotificationTransport):
			// Matched and would have delivered, but this environment has
			// nowhere to send it — an honest out-of-scope skip (§3.3),
			// distinct from both a Match/Plan condition-declined skip
			// (recordSkip) and a real 'failed' — nothing went wrong here.
			detail, err := reasonDetail("no notification transport configured")
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'skipped', detail = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, runKey(h, ev), detail)
			return err
		case applyErr != nil:
			detail, err := reasonDetail(applyErr.Error())
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE workflow_run SET status = 'failed', detail = $3
				WHERE handler = $1 AND idempotency_key = $2`, h.Spec().Name, runKey(h, ev), detail)
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
}

// claimRun claims the (handler, key) row FIRST: whoever inserts runs; a
// redelivery finds the claim and stops (AC-W3). Terminal-at-claim
// outcomes (skipped, failed before Apply) insert directly in their final
// state, so the claim doubles as the honest run record. detail is the
// raw jsonb payload (rundetail.go) — nil for a run with no reason yet.
//
// trigger_event is provenance, not the dedupe key (idempotency_key is):
// an event trigger's ev.ID is the bus envelope's real event id, while a
// clock trigger has no source event — its caller (the time-scan, a
// later slice) must synthesize a fresh ids.NewV7() per evaluation pass
// so this column never writes the zero UUID (workflow_run.trigger_event
// is NOT NULL; a silently-wrong provenance is the honest-hard-case this
// column guards against).
func (e *WorkflowEngine) claimRun(ctx context.Context, h workflow.Handler, ev workflow.Event, planned []byte, status string, detail []byte) (bool, error) {
	claimed := false
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO workflow_run (workspace_id, handler, idempotency_key, trigger_event, planned, status, detail)
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
	payload, err := reasonDetail(detail)
	if err != nil {
		return errors.Join(cause, err)
	}
	if _, claimErr := e.claimRun(ctx, h, ev, emptyPlan, "failed", payload); claimErr != nil {
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
//
// Only called for EVENT triggers (runOne guards the clock case above):
// an event's key carries its own ev.ID, so a skip row here claims a key
// no future delivery of the SAME event will ever need to reuse — unlike
// a clock trigger's anchor key, which a later true evaluation must still
// be able to claim.
func (e *WorkflowEngine) recordSkip(ctx context.Context, h workflow.Handler, ev workflow.Event) error {
	if ev.AutomationID == ids.Nil {
		return nil
	}
	detail, err := reasonDetail("the trigger event did not satisfy this automation's conditions")
	if err != nil {
		return err
	}
	_, err = e.claimRun(ctx, h, ev, emptyPlan, "skipped", detail)
	return err
}
