// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The starter workflow library (features/03 §5.3): small, deterministic
// automations shipped as ordinary handlers — the worked examples the
// `crm gen workflow` scaffold copies, not privileged built-ins.

import (
	"context"
	"encoding/json"

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

// stageChangeCreateTask keeps momentum: every stage move mints a
// follow-up task on the deal's own timeline.
type stageChangeCreateTask struct {
	ex Executors
}

func (stageChangeCreateTask) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    stageChangeCreateTaskName,
		Trigger: workflow.Trigger{EventType: eventDealStageChanged},
		Tier:    mcp.TierGreen,
	}
}

func (stageChangeCreateTask) Match(_ context.Context, ev workflow.Event) (bool, error) {
	// A close (won/lost) ends the cadence; only open moves need a next
	// step.
	var payload struct {
		ToSemantic string `json:"to_semantic"`
	}
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			return false, err
		}
	}
	return payload.ToSemantic == "" || payload.ToSemantic == "open", nil
}

// fieldKind is the "kind" json key shared by the action snapshot and the
// task effects handlers plan — named once so the string doesn't drift.
const fieldKind = "kind"

func (w stageChangeCreateTask) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	dueInDays, err := DueInDays(ev.Params, 2)
	if err != nil {
		return workflow.Effect{}, err
	}
	args, err := json.Marshal(map[string]any{
		fieldKind: "task",
		"subject": "Plan the next step after the stage change",
		"due_at":  ev.OccurredAt.AddDate(0, 0, dueInDays),
		"links": []map[string]any{{
			"entity_type": "deal", "entity_id": ev.Entity.ID,
		}},
	})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
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
		Tier:    mcp.TierGreen,
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
	args, err := json.Marshal(map[string]any{
		fieldKind: "task",
		"subject": "Follow up with the new lead",
		"due_at":  ev.OccurredAt.AddDate(0, 0, dueInDays),
		"links": []map[string]any{{
			"entity_type": string(ev.Entity.Type), "entity_id": ev.Entity.ID,
		}},
	})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

func (w routeLeadCreateTask) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (routeLeadCreateTask) IdempotencyKey(ev workflow.Event) string {
	return routeLeadName + ":" + ev.ID.String()
}
