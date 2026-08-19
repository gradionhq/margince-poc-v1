// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// LeadSLAWorkflows returns the system handlers that read a captured
// activity against the leads it touches: one stamps the first response off
// an outbound activity (formulas §18.1) — the one first-response trigger
// that arrives as an event rather than as a lead write — and one climbs the
// status ladder (an outbound touch makes a new lead contacted; an inbound
// reply or a booked or held meeting makes an open lead engaged). Same shape
// as LeadScoreWorkflows: every activity.captured is matched, and whether it
// touches a lead is the Apply-side query.
func LeadSLAWorkflows(store *Store) []workflow.Handler {
	return []workflow.Handler{leadFirstResponse{store: store}, leadStatusLadder{store: store}}
}

type leadFirstResponse struct{ store *Store }

func (leadFirstResponse) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "lead_first_response",
		Trigger: workflow.Trigger{EventType: "activity.captured"},
		Tier:    mcp.TierAutoExecute,
	}
}

func (leadFirstResponse) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (leadFirstResponse) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionUpdateRecord, Target: ev.Entity,
	}}}, nil
}

func (w leadFirstResponse) Apply(ctx context.Context, ev workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	touches, err := w.store.leadResponseTouches(ctx, ids.From[ids.ActivityKind](ev.Entity.ID))
	if err != nil {
		return workflow.RunResult{}, err
	}
	applied := false
	for _, t := range touches {
		if !isFirstResponseActivity(t.direction, t.capturedBy, t.hadInbound) {
			continue
		}
		set, err := w.store.RecordLeadFirstResponse(ctx, t.lead, t.occurredAt)
		if err != nil {
			return workflow.RunResult{}, fmt.Errorf("record first response on lead %s: %w", t.lead, err)
		}
		applied = applied || set
	}
	if !applied {
		return workflow.RunResult{}, nil
	}
	return workflow.RunResult{Applied: eff.Actions}, nil
}

func (leadFirstResponse) IdempotencyKey(ev workflow.Event) string {
	return "lead_first_response:" + ev.ID.String()
}

// leadStatusLadder climbs the open ladder from captured activity. It never
// steps down and never touches a terminal lead; a human may still place the
// lead by hand, and the row records which of the two last moved it.
type leadStatusLadder struct{ store *Store }

func (leadStatusLadder) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "lead_status_ladder",
		Trigger: workflow.Trigger{EventType: "activity.captured"},
		Tier:    mcp.TierAutoExecute,
	}
}

func (leadStatusLadder) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (leadStatusLadder) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionUpdateRecord, Target: ev.Entity,
	}}}, nil
}

func (w leadStatusLadder) Apply(ctx context.Context, ev workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	touches, err := w.store.leadResponseTouches(ctx, ids.From[ids.ActivityKind](ev.Entity.ID))
	if err != nil {
		return workflow.RunResult{}, err
	}
	applied := false
	for _, t := range touches {
		target, ok := ladderStepFor(t)
		if !ok {
			continue
		}
		moved, err := w.store.AdvanceLeadStatus(ctx, t.lead, target, t.occurredAt)
		if err != nil {
			return workflow.RunResult{}, fmt.Errorf("advance lead %s to %s: %w", t.lead, target, err)
		}
		applied = applied || moved
	}
	if !applied {
		return workflow.RunResult{}, nil
	}
	return workflow.RunResult{Applied: eff.Actions}, nil
}

func (leadStatusLadder) IdempotencyKey(ev workflow.Event) string {
	return "lead_status_ladder:" + ev.ID.String()
}

// ladderStepFor reads one touch as the step it earns: an inbound reply or a
// booked/held meeting is engagement; any other outbound touch is contact. A
// system-captured outbound with no prior inbound — a cold sequence mail — is
// still contact: we reached out, whoever pressed send.
func ladderStepFor(t leadResponseTouch) (LeadStatus, bool) {
	switch {
	case t.direction == "inbound", t.kind == "meeting" && (t.meetingStatus == "booked" || t.meetingStatus == "held"):
		return LeadStatusEngaged, true
	case t.direction == "outbound":
		return LeadStatusContacted, true
	}
	return "", false
}

// leadResponseTouch is one (activity, lead) pair with what §18.1 needs to
// decide whether the activity was a genuine response, and what the ladder
// needs to decide which step it earns.
type leadResponseTouch struct {
	lead          ids.LeadID
	direction     string
	capturedBy    string
	occurredAt    time.Time
	hadInbound    bool
	kind          string
	meetingStatus string
}

// leadResponseTouches answers which leads the activity is linked to, with
// the activity's direction and author, and whether the lead had already
// written in before it — usually none.
func (s *Store) leadResponseTouches(ctx context.Context, activityID ids.ActivityID) ([]leadResponseTouch, error) {
	var out []leadResponseTouch
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT l.lead_id, coalesce(a.direction, ''), a.captured_by, a.occurred_at,
			       EXISTS (SELECT 1 FROM activity_link li JOIN activity ai ON ai.id = li.activity_id
			               WHERE li.lead_id = l.lead_id AND ai.direction = 'inbound'
			                 AND ai.archived_at IS NULL AND ai.occurred_at < a.occurred_at),
			       a.kind, coalesce(a.meeting_status, '')
			FROM activity_link l JOIN activity a ON a.id = l.activity_id
			WHERE l.activity_id = $1 AND l.lead_id IS NOT NULL AND a.archived_at IS NULL`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t leadResponseTouch
			if err := rows.Scan(&t.lead, &t.direction, &t.capturedBy, &t.occurredAt, &t.hadInbound, &t.kind, &t.meetingStatus); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// AdvanceLeadStatus climbs the open ladder for one lead from captured
// activity: the move happens only when target is a step ABOVE the current
// one and the lead is open, so a replayed or out-of-order event can never
// pull a lead back, and a terminal lead is never touched. Answers whether
// this call moved it. One audit row and lead.updated, under the row lock,
// with status_set_by recording that the system did it.
func (s *Store) AdvanceLeadStatus(ctx context.Context, leadID ids.LeadID, target LeadStatus, at time.Time) (bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return false, err
	}
	moved := false
	err := s.tx(ctx, func(tx pgx.Tx) error {
		lock, err := storekit.LockRow(ctx, tx, "lead", leadID.UUID, storekit.LiveOnly)
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil // archived or gone: a terminal lead owes no step
		}
		if err != nil {
			return err
		}
		var current string
		var setBy *string
		if err := tx.QueryRow(ctx, `SELECT status, status_set_by FROM lead WHERE id = $1`, leadID).Scan(&current, &setBy); err != nil {
			return err
		}
		if !LeadStatus(current).Advances(target) {
			return nil
		}
		p := storekit.NewPatch()
		p.Set(leadStatusColumn, current, string(target))
		p.Set(leadStatusSetByColumn, setBy, string(crmcontracts.LeadStatusSetBySystem))
		if err := p.ApplyLocked(ctx, tx, lock); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID, p.Before(), p.After())
		if err != nil {
			return err
		}
		moved = true
		return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
			ChangedFields: map[string]any{eventKeyDelta: map[string]any{leadStatusColumn: string(target), "at": at}},
		})
	})
	return moved, err
}
