// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// LeadSLAWorkflows returns the system handler that stamps a lead's first
// response off an outbound activity (formulas §18.1) — the one first-
// response trigger that arrives as an event rather than as a lead write.
// Same shape as LeadScoreWorkflows: every activity.captured is matched, and
// whether it touches a lead is the Apply-side query.
func LeadSLAWorkflows(store *Store) []workflow.Handler {
	return []workflow.Handler{leadFirstResponse{store: store}}
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

// leadResponseTouch is one (activity, lead) pair with what §18.1 needs to
// decide whether the activity was a genuine response.
type leadResponseTouch struct {
	lead       ids.LeadID
	direction  string
	capturedBy string
	occurredAt time.Time
	hadInbound bool
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
			                 AND ai.archived_at IS NULL AND ai.occurred_at < a.occurred_at)
			FROM activity_link l JOIN activity a ON a.id = l.activity_id
			WHERE l.activity_id = $1 AND l.lead_id IS NOT NULL AND a.archived_at IS NULL`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t leadResponseTouch
			if err := rows.Scan(&t.lead, &t.direction, &t.capturedBy, &t.occurredAt, &t.hadInbound); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}
