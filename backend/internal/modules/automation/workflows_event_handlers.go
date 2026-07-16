// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The two event-driven starter templates beside stage_change_create_task
// (workflows_starter.go): stage_change_notify and post_meeting_recap. Split
// into their own file per that file's own growth note, rather than letting
// the starter library accrete handlers into one ever-longer switch of structs.

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
		Tier:    mcp.TierGreen,
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
