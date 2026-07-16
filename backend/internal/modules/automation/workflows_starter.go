// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The starter workflow library (features/03 §5.3): small, deterministic
// automations shipped as ordinary handlers — the worked examples the
// `crm gen workflow` scaffold copies, not privileged built-ins.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// StarterWorkflows returns the shipped handler set over the injected
// record seam. The route_lead starter is NOT here: its engine is
// lead-store SQL, so the people module provides that handler and
// compose registers it beside these. approvals is the staging seam
// every handler's Apply threads into ApplyActions, whether or not this
// particular starter's own effect ever lands a 🟡 action.
func StarterWorkflows(provider datasource.SystemOfRecordProvider, approvals Approvals) []workflow.Handler {
	return []workflow.Handler{
		stageChangeCreateTask{provider: provider, approvals: approvals},
	}
}

// stageChangeCreateTask keeps momentum: every stage move mints a
// follow-up task on the deal's own timeline.
type stageChangeCreateTask struct {
	provider  datasource.SystemOfRecordProvider
	approvals Approvals
}

func (stageChangeCreateTask) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "stage_change_create_task",
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

func (w stageChangeCreateTask) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	dueInDays, err := DueInDays(ev.Params, 2)
	if err != nil {
		return workflow.Effect{}, err
	}
	args, err := json.Marshal(map[string]any{
		"kind":    "task",
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
	applied, err := ApplyActions(ctx, w.provider, w.approvals, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (stageChangeCreateTask) IdempotencyKey(ev workflow.Event) string {
	return "stage_change_create_task:" + ev.ID.String()
}
