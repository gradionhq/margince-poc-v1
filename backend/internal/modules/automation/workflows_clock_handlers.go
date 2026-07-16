// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The first clock-triggered starter (Task 14a, features/10 §1's
// no_activity_for_n_days / UC-E15-01's own worked example, "remind me if
// a deal I own has no activity for N days"). Unlike the event starters
// (workflows_starter.go, workflows_event_handlers.go), it has no event to
// read Match's decision off: TimeScanner (timescan.go) is a coarse SQL
// pre-filter over ActivityScan, and Match here is the PRECISE re-check
// every clock handler owes (occurrence_test.go's convention) — re-deriving
// the SAME N-day cutoff from ev.Params and confirming the anchor the scan
// carried is genuinely stale enough.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// noActivityReminderName is the catalog key Task 6 seeds this starter
// under — CatalogEntry.Key must equal the backing handler's Spec().Name,
// one vocabulary across the catalog, the engine, and run records
// (automations_catalog.go's CatalogEntry doc).
const noActivityReminderName = "no_activity_reminder"

// noActivityScheduleMarker is Trigger.Schedule's value. RegisterWorkflow
// (workflows.go) only requires Schedule to be non-empty — that non-empty-
// ness is the marker isClockTrigger (workflows_run.go) routes on to reach
// the time-scan instead of the bus. The actual cadence is the River
// periodic job's own interval (compose/jobs.go's TimeScanArgs), so this
// string documents intent for a human reading the registry; it is never
// parsed as a cron expression.
const noActivityScheduleMarker = "clock:no_activity_scan"

// defaultNoActivityDays is the fallback "how many quiet days" threshold —
// UC-E15-01's own worked example sets N=7 when a user turns this on.
const defaultNoActivityDays = 7

// noActivityDays reads the "how many quiet days" knob off an automation
// instance's params. Both TimeScanner.Scan (timescan.go, which needs N to
// build its SQL cutoff for the coarse pre-filter) and this handler's own
// Match (the precise recheck) call this SAME function — one reader, so
// the coarse scan and the exact decision can never drift onto two
// different thresholds for the identical instance.
func noActivityDays(params json.RawMessage) (int, error) {
	if len(params) == 0 {
		return defaultNoActivityDays, nil
	}
	var decoded struct {
		NoActivityDays *int `json:"no_activity_days"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return 0, fmt.Errorf("automation: no_activity_reminder params: %w", err)
	}
	if decoded.NoActivityDays == nil {
		return defaultNoActivityDays, nil
	}
	return *decoded.NoActivityDays, nil
}

// noActivityAnchorPayload is the wire shape the stale anchor rides in
// workflow.Event.Payload for one no_activity_reminder clock pass
// (timescan.go's buildNoActivityEvent writes it; noActivityAnchor below
// reads it back). The anchor lives in idempotency_key, NOT trigger_event
// (workflows_run.go's claimRun doc: trigger_event is a fresh per-pass id,
// pure provenance) — this is what makes the firing re-arm exactly when
// the entity's last touch moves and stay quiet while it doesn't (Task
// 12's occurrence-key contract, occurrence_test.go).
type noActivityAnchorPayload struct {
	LastActivityAt time.Time `json:"last_activity_at"`
}

// errNoActivityAnchorMissing is a wiring bug, not a routine non-match:
// every event this handler ever sees was built by
// timescan.go's buildNoActivityEvent, which always sets Payload. A caller
// that skipped that path (a hand-built event in a test, say) gets a loud
// error rather than a silently-false Match.
var errNoActivityAnchorMissing = errors.New("automation: no_activity_reminder: event carries no anchor payload")

// noActivityAnchor decodes the stale anchor a clock pass carried in
// ev.Payload — the one reader Match, Plan, and IdempotencyKey all share.
func noActivityAnchor(ev workflow.Event) (time.Time, error) {
	if len(ev.Payload) == 0 {
		return time.Time{}, errNoActivityAnchorMissing
	}
	var payload noActivityAnchorPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return time.Time{}, fmt.Errorf("automation: decoding the no_activity anchor: %w", err)
	}
	return payload.LastActivityAt, nil
}

// noActivityReminder reminds an entity's owner once its most recent
// captured activity has gone quiet for N days — the clock counterpart of
// the event starters, converging on the identical runOne path
// (workflows_run.go) via TimeScanner (timescan.go) rather than the bus.
type noActivityReminder struct {
	ex Executors
}

func (noActivityReminder) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    noActivityReminderName,
		Trigger: workflow.Trigger{Schedule: noActivityScheduleMarker},
		Tier:    mcp.TierGreen,
	}
}

// Match is the precise re-check TimeScanner's SQL cutoff only
// approximates as a coarse pre-filter: re-derive N from ev.Params
// (identically to the scan) and confirm the anchor is genuinely before
// now-N-days as of this event's own OccurredAt (the scan's captured-once
// "now" — occurrence_test.go's convention).
func (noActivityReminder) Match(_ context.Context, ev workflow.Event) (bool, error) {
	anchor, err := noActivityAnchor(ev)
	if err != nil {
		return false, err
	}
	days, err := noActivityDays(ev.Params)
	if err != nil {
		return false, err
	}
	cutoff := ev.OccurredAt.AddDate(0, 0, -days)
	return anchor.Before(cutoff), nil
}

// Plan mints one create_task reminder anchored on the stale entity,
// naming the anchor in the subject so the reminder is never a mystery
// number (P6) — the same create_task shape stageChangeCreateTask
// (workflows_starter.go) builds, generalized to whatever entity type
// actually fired rather than a hardcoded "deal".
func (noActivityReminder) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	anchor, err := noActivityAnchor(ev)
	if err != nil {
		return workflow.Effect{}, err
	}
	args, err := json.Marshal(map[string]any{
		"kind":    "task",
		"subject": fmt.Sprintf("Check in — no activity since %s", anchor.Format(time.DateOnly)),
		"due_at":  ev.OccurredAt,
		"links": []map[string]any{{
			"entity_type": string(ev.Entity.Type), "entity_id": ev.Entity.ID,
		}},
	})
	if err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: encoding the no_activity_reminder task: %w", err)
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

func (w noActivityReminder) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

// IdempotencyKey is the load-bearing occurrence key (Task 12): derived
// from the ANCHOR, never ev.ID — a fresh time-scan pass mints a new ev.ID
// every tick regardless of whether the entity's last touch actually
// moved, so keying on it would refire this reminder every single pass
// while the condition stays true. Keying on the anchor means the SAME
// stale last_activity_at claims the SAME row (claimRun's ON CONFLICT DO
// NOTHING absorbs every redundant pass), and only a NEW anchor — the
// entity was touched again, then went quiet a second time — re-arms it.
func (noActivityReminder) IdempotencyKey(ev workflow.Event) string {
	anchor, err := noActivityAnchor(ev)
	if err != nil {
		// workflow.Handler's IdempotencyKey has no error return (ports/workflow/workflow.go);
		// every real caller (runOne, reached only after Match already
		// decoded this same payload successfully) never hits this branch.
		// Folding the error into the key itself — rather than a fixed
		// placeholder — means a caller that skipped Match still can't
		// silently collide two different failures onto one claimed row.
		return noActivityReminderName + ":anchor-error:" + err.Error()
	}
	return noActivityReminderName + ":anchor:" + anchor.UTC().Format(time.RFC3339Nano)
}
