// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// stage_change_notify: the second event-driven starter template beside
// stage_change_create_task (workflows_starter.go). Split into its own
// file per that file's own growth note, rather than letting the starter
// library accrete handlers into one ever-longer switch of structs.
//
// post_meeting_recap (the other half of this template pair) is NOT here:
// its honest Match needs the activity.updated event to show a meeting's
// meeting_status transitioning to held, but neither UpdateActivityInput
// (activities/lifecycle.go) nor the UpdateActivityRequest contract schema
// (api/crm.yaml) carries a meeting_status field at all — the column is
// write-once at creation. Building Match against a delta shape the wire
// contract cannot produce would be exactly the silent-Match bug this
// template is warned against; it needs the update path extended upstream
// first, not a workaround here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// dealOwnerFields is the one column stage_change_notify needs off the
// moved deal's record. Decoding straight into this local shape — rather
// than the full wire Deal type — keeps the handler from taking on a
// contract-type dependency it has no other use for.
type dealOwnerFields struct {
	OwnerID *ids.UUID `json:"owner_id"`
}

// errDealHasNoOwner is stage_change_notify's Plan failure when the moved
// deal carries no owner: notify has no honest recipient to name, and a
// nil/zero recipient would be a fabricated one, not a resolved one — the
// run lands 'failed' with this reason rather than a silently empty notify.
var errDealHasNoOwner = errors.New("automation: stage_change_notify: deal has no assigned owner to notify")

// stageChangeNotify tells the deal's owner about every stage move,
// including the closes (won/lost) that end stageChangeCreateTask's own
// follow-up cadence — a rep especially wants to hear that their own deal
// closed, not just that it is still open.
type stageChangeNotify struct {
	ex Executors
}

func (stageChangeNotify) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "stage_change_notify",
		Trigger: workflow.Trigger{EventType: eventDealStageChanged},
		Tier:    mcp.TierGreen,
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
	return "stage_change_notify:" + ev.ID.String()
}
