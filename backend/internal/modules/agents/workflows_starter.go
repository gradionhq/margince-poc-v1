package agents

// The starter workflow library (features/03 §5.3): small, deterministic
// automations shipped as ordinary handlers — the worked examples the
// `crm gen workflow` scaffold copies, not privileged built-ins.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// StarterWorkflows returns the shipped handler set over the injected
// record seam.
func StarterWorkflows(provider datasource.SystemOfRecordProvider) []workflow.Handler {
	return []workflow.Handler{
		routeLead{provider: provider},
		stageChangeCreateTask{provider: provider},
	}
}

// routeLead answers every new lead with a triage task — the "no lead
// sits unseen" floor until territory routing rules arrive.
type routeLead struct {
	provider datasource.SystemOfRecordProvider
}

func (routeLead) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "route_lead",
		Trigger: workflow.Trigger{EventType: "lead.created"},
		Tier:    mcp.TierGreen,
	}
}

func (routeLead) Match(context.Context, workflow.Event) (bool, error) {
	return true, nil // every fresh lead deserves eyes
}

func (w routeLead) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	args, err := json.Marshal(map[string]any{
		"kind":    "task",
		"subject": fmt.Sprintf("Triage new lead %s", ev.Entity.ID),
		"due_at":  ev.OccurredAt.AddDate(0, 0, 1),
	})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

func (w routeLead) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.provider, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (routeLead) IdempotencyKey(ev workflow.Event) string {
	return "route_lead:" + ev.Entity.ID.String()
}

// stageChangeCreateTask keeps momentum: every stage move mints a
// follow-up task on the deal's own timeline.
type stageChangeCreateTask struct {
	provider datasource.SystemOfRecordProvider
}

func (stageChangeCreateTask) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "stage_change_create_task",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
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
	args, err := json.Marshal(map[string]any{
		"kind":    "task",
		"subject": "Plan the next step after the stage change",
		"due_at":  ev.OccurredAt.AddDate(0, 0, 2),
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
	applied, err := ApplyActions(ctx, w.provider, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (stageChangeCreateTask) IdempotencyKey(ev workflow.Event) string {
	return "stage_change_create_task:" + ev.ID.String()
}
