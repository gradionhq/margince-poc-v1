// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The event-triggered starter workflows (features/03 §5.3): small,
// deterministic automations shipped as ordinary handlers — the worked
// examples the `crm gen workflow` scaffold copies, not privileged
// built-ins. Every handler here rides the bus (a Trigger.EventType); the
// clock-triggered siblings live in handlers_clock.go. The four are
// stage_change_create_task and route_lead (task minters),
// stage_change_notify (the owner notification), and post_meeting_recap
// (the draft_email starter).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// StarterWorkflows returns the shipped handler set over the injected
// executor seams. assign_lead_owner is NOT here: its engine is
// lead-store SQL, so the people module provides that handler (under
// its own honest name — AUTO-NOTE-2, §3.5) and compose registers it
// beside these. ex is the seam bundle every handler's Apply threads
// into ApplyActions, whether or not this particular starter's own
// effect ever exercises every seam in it.
func StarterWorkflows(ex Executors) []workflow.Handler {
	return []workflow.Handler{
		stageChangeCreateTask{ex: ex},
		stageChangeNotify{ex: ex},
		postMeetingRecap{ex: ex},
		noActivityReminder{ex: ex},
		checkInCadence{ex: ex},
		renewalReminder{ex: ex},
		routeLeadCreateTask{ex: ex},
	}
}

// stageChangeCreateTaskName is the catalog key Task 6 seeds this
// starter under — CatalogEntry.Key must equal the backing handler's
// Spec().Name (automations_catalog.go's CatalogEntry doc).
const stageChangeCreateTaskName = "stage_change_create_task"

// stageChangeCreateTask keeps momentum: every OPEN stage move mints a
// follow-up task on the deal's own timeline (a close ends the cadence).
type stageChangeCreateTask struct {
	ex Executors
}

// dealStageStatusField is the deal.stage_changed payload key carrying the
// destination stage's semantic class, and dealStatusOpen is the one value
// that keeps stage_change_create_task's follow-up cadence going. deals
// emits the class under "to_status" (deals/deal_advance.go's Emit: open |
// won | lost) — NOT "to_semantic", which no emitter ever sets. Both are
// named once here and reused by the preview's equivalent predicate
// (automations_preview.go's dealPreviewDefs) so the runtime filter and its
// dry-run can never again drift onto a different field or value.
const (
	dealStageStatusField = "to_status"
	dealStatusOpen       = "open"
)

func (stageChangeCreateTask) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    stageChangeCreateTaskName,
		Trigger: workflow.Trigger{EventType: eventDealStageChanged},
		Tier:    mcp.TierAutoExecute,
	}
}

func (stageChangeCreateTask) Match(_ context.Context, ev workflow.Event) (bool, error) {
	// A close (won/lost) ends the cadence; only open moves need a next
	// step. An absent payload (defensive — every real deal.stage_changed
	// carries one) is treated as an open move rather than silently dropped.
	if len(ev.Payload) == 0 {
		return true, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return false, err
	}
	status, _ := payload[dealStageStatusField].(string)
	return status == "" || status == dealStatusOpen, nil
}

// fieldKind is the "kind" json key shared by the action snapshot and the
// task effects handlers plan — named once so the string doesn't drift.
const fieldKind = "kind"

func (w stageChangeCreateTask) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	dueInDays, err := DueInDays(ev.Params, 2)
	if err != nil {
		return workflow.Effect{}, err
	}
	// The fired entity IS the deal (this handler triggers only on
	// deal.stage_changed), so taskCreateEffect's string(ev.Entity.Type)
	// resolves to "deal" — the same value this map hardcoded before the
	// shared builder existed.
	return taskCreateEffect(ev, "Plan the next step after the stage change",
		ev.OccurredAt.AddDate(0, 0, dueInDays))
}

func (w stageChangeCreateTask) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (stageChangeCreateTask) IdempotencyKey(ev workflow.Event) string {
	return stageChangeCreateTaskName + ":" + ev.ID.String()
}

// routeLeadName is the catalog key Task 6 seeds this starter under
// (AUTO-NOTE-2, §3.5): "route a new lead to a task", the CREATE_TASK
// reading of those words — never confused with the OWNER-assignment
// reading of the same phrase, which lives under its own honest name,
// assign_lead_owner (people.LeadRoutingWorkflow), because the two are
// genuinely different acts, not two names for one automation.
const routeLeadName = "route_lead"

// defaultRouteLeadDueInDays is route_lead's own due_in_days default — a
// brand-new lead wants its first follow-up sooner than a mid-pipeline
// deal's stage-change nudge (stageChangeCreateTask's own default of 2).
const defaultRouteLeadDueInDays = 1

// routeLeadCreateTask mints a follow-up task the moment a lead is
// captured, so a brand-new lead always gets a first next-step — the
// create_task sibling of stageChangeCreateTask, over lead.created
// instead of a stage move.
type routeLeadCreateTask struct {
	ex Executors
}

func (routeLeadCreateTask) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    routeLeadName,
		Trigger: workflow.Trigger{EventType: eventLeadCreated},
		Tier:    mcp.TierAutoExecute,
	}
}

// Match fires unconditionally: unlike stage_change_create_task (which
// narrows to open moves), every new lead needs its first follow-up —
// there is no "wrong direction" a lead.created could have arrived from.
func (routeLeadCreateTask) Match(_ context.Context, _ workflow.Event) (bool, error) {
	return true, nil
}

func (w routeLeadCreateTask) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	dueInDays, err := DueInDays(ev.Params, defaultRouteLeadDueInDays)
	if err != nil {
		return workflow.Effect{}, err
	}
	return taskCreateEffect(ev, "Follow up with the new lead",
		ev.OccurredAt.AddDate(0, 0, dueInDays))
}

func (w routeLeadCreateTask) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (routeLeadCreateTask) IdempotencyKey(ev workflow.Event) string {
	return routeLeadName + ":" + ev.ID.String()
}

// dealOwnerFields is the one column stage_change_notify needs off the
// moved deal's record. Decoding straight into this local shape — rather
// than the full wire Deal type — keeps the handler from taking on a
// contract-type dependency it has no other use for.
type dealOwnerFields struct {
	OwnerID *ids.UUID `json:"owner_id"`
}

// errDealHasNoOwner is stage_change_notify's Plan decline when the moved
// deal carries no owner: notify has no honest recipient to name, and a
// nil/zero recipient would be a fabricated one, not a resolved one. This is a
// skip, not a failure — the deal legitimately has no owner and a redelivery
// would meet the same record — so the run lands 'skipped' with this reason
// (visible history, no phantom recipient) rather than a 'failed' that would
// leave the event re-delivering forever and poison its sibling handlers.
var errDealHasNoOwner = declineFiring("the moved deal has no assigned owner to notify")

// stageChangeNotifyName is the catalog key Task 6 seeds this starter
// under — CatalogEntry.Key must equal the backing handler's Spec().Name
// (automations_catalog.go's CatalogEntry doc).
const stageChangeNotifyName = "stage_change_notify"

// stageChangeNotify tells the deal's owner about every stage move,
// including the closes (won/lost) that end stageChangeCreateTask's own
// follow-up cadence — a rep especially wants to hear that their own deal
// closed, not just that it is still open.
type stageChangeNotify struct {
	ex Executors
}

func (stageChangeNotify) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    stageChangeNotifyName,
		Trigger: workflow.Trigger{EventType: eventDealStageChanged},
		Tier:    mcp.TierAutoExecute,
	}
}

// Match fires unconditionally on every stage move: unlike
// stageChangeCreateTask (which narrows to open moves — a closed deal
// needs no next-step task), a notification's whole point is that the
// owner hears about the move regardless of which way it went.
func (stageChangeNotify) Match(_ context.Context, _ workflow.Event) (bool, error) {
	return true, nil
}

func (w stageChangeNotify) Plan(ctx context.Context, ev workflow.Event) (workflow.Effect, error) {
	rec, err := w.ex.Provider.Read(ctx, ev.Entity)
	if err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: reading the moved deal: %w", err)
	}
	var deal dealOwnerFields
	if err := json.Unmarshal(rec.Fields, &deal); err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: decoding the deal's owner: %w", err)
	}
	if deal.OwnerID == nil {
		return workflow.Effect{}, errDealHasNoOwner
	}
	args, err := json.Marshal(notifyArgs{
		Recipient: *deal.OwnerID,
		Subject:   "A deal you own changed stage",
		Body:      fmt.Sprintf("Deal %s moved to a new pipeline stage.", ev.Entity.ID),
	})
	if err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: encoding the notify action: %w", err)
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionNotify, Target: ev.Entity, Args: args,
	}}}, nil
}

func (w stageChangeNotify) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (stageChangeNotify) IdempotencyKey(ev workflow.Event) string {
	return stageChangeNotifyName + ":" + ev.ID.String()
}

// activityKindMeeting is the wire value activity.captured carries in its
// payload's "kind" field for a logged meeting (mirrors the contract's
// ActivityKind enum). Named here because post_meeting_recap's Match pins to
// it and a bare string literal would be a goconst finding besides.
const activityKindMeeting = "meeting"

// recapIntent is the intent post_meeting_recap hands the draft_email
// executor — the same "what to write" hint applyDraftEmail (handlers_actions.go)
// reads off draftEmailArgs.Intent and threads into Comms.DraftEmail.
const recapIntent = "post-meeting recap"

// capturedActivityKind is the one field post_meeting_recap's Match reads off
// the activity.captured payload (activities/activity.go emits {"kind": …}).
type capturedActivityKind struct {
	Kind string `json:"kind"`
}

// postMeetingRecap drafts a follow-up recap whenever a meeting is logged.
//
// It fires on activity.captured (the first-class capture verb, whose payload
// carries the activity's kind) and matches kind=meeting — a captured meeting
// IS the "the meeting happened, write it up" signal, and the kind rides the
// event so Match never reads the store.
//
// Honest limitation: activity.captured carries no meeting_status, so this
// cannot tell a logged-past meeting (held) from one captured while still
// booked in the future — it fires on ANY captured meeting. A first-class
// meeting-concluded event, or meeting_status in the capture payload, would
// let it narrow to concluded meetings; until the wire contract carries that,
// meeting capture is the available signal and this triggers on it.
type postMeetingRecap struct {
	ex Executors
}

// postMeetingRecapName is the catalog key Task 6 seeds this starter
// under — CatalogEntry.Key must equal the backing handler's Spec().Name
// (automations_catalog.go's CatalogEntry doc).
const postMeetingRecapName = "post_meeting_recap"

func (postMeetingRecap) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    postMeetingRecapName,
		Trigger: workflow.Trigger{EventType: eventActivityCaptured},
		Tier:    mcp.TierAutoExecute,
	}
}

// Match reads the captured activity's kind straight off the event payload —
// true for a meeting, false for every other kind (call, email, note, …) — so
// a recap is drafted only when a meeting was logged, with no store read.
func (postMeetingRecap) Match(_ context.Context, ev workflow.Event) (bool, error) {
	var payload capturedActivityKind
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			return false, fmt.Errorf("automation: decoding the captured activity kind: %w", err)
		}
	}
	return payload.Kind == activityKindMeeting, nil
}

func (postMeetingRecap) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	args, err := json.Marshal(draftEmailArgs{Intent: recapIntent})
	if err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: encoding the draft_email action: %w", err)
	}
	// Target is the captured meeting activity itself: applyDraftEmail anchors
	// the composed draft on it (Comms.DraftEmail's anchor), and the draft
	// lands durably on workflow_run.applied.
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionDraftEmail, Target: ev.Entity, Args: args,
	}}}, nil
}

func (w postMeetingRecap) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (postMeetingRecap) IdempotencyKey(ev workflow.Event) string {
	return postMeetingRecapName + ":" + ev.ID.String()
}
