// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Behavioral lead-score recompute (formulas-and-rules §3): every
// captured or updated activity that is LINKED TO A LEAD re-runs the §3
// weighted-signal formula for that lead — replies read the real
// activity.direction column, meetings the real meeting_status column;
// opens/clicks stay 0 until the deferred engagement_event substrate
// exists (the spec's own column-readiness note). Registered as a SYSTEM
// workflow: a formula invariant, always on, never a pausable user
// automation.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// RecomputeLeadScore re-runs §3 for one live lead from its linked
// activities and persists the change with audit + lead.updated.
func (s *Store) RecomputeLeadScore(ctx context.Context, leadID ids.LeadID, now time.Time) error {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return recomputeLeadScoreTx(ctx, tx, leadID, now)
	})
}

// recomputeLeadScoreTx is the §3 recompute inside an open transaction —
// shared by the SYSTEM workflow lane and the override-clear path in
// UpdateLead. When a Commercial Judgement override is in force (a
// non-empty score_override_reason) it NEVER overwrites lead.score: the
// human value is sticky (formulas §3.1, AC-S1) and the freshly machine
// value is retained in score_computed instead. With no override, score
// tracks the machine value directly (score_computed stays null).
func recomputeLeadScoreTx(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, now time.Time) error {
	var title, source, overrideReason *string
	var currentScore int
	var currentComputed *int
	var status string
	err := tx.QueryRow(ctx,
		`SELECT title, source, score, score_override_reason, score_computed, status
		   FROM lead WHERE id = $1 AND archived_at IS NULL`,
		leadID).Scan(&title, &source, &currentScore, &overrideReason, &currentComputed, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // archived or gone: nothing to score
	}
	if err != nil {
		return err
	}
	if !LeadStatus(status).Open() {
		return nil // promoted/disqualified leads keep their last score
	}

	signals, err := leadBehavioralSignals(ctx, tx, leadID)
	if err != nil {
		return err
	}
	machine, _ := ScoreLead(deref(title), deref(source), signals, now)

	// Sticky override: the machine value moves score_computed, never score.
	if overrideReason != nil {
		if currentComputed != nil && *currentComputed == machine {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE lead SET score_computed = $2 WHERE id = $1`, leadID, machine); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
			map[string]any{"score_computed": currentComputed}, map[string]any{"score_computed": machine})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.WebhookPayloadLeadUpdated{
			ChangedFields: map[string]any{eventKeyDelta: map[string]any{"score_computed": machine}},
		})
	}

	if machine == currentScore {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE lead SET score = $2 WHERE id = $1`, leadID, machine); err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
		map[string]any{"score": currentScore}, map[string]any{"score": machine})
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.WebhookPayloadLeadUpdated{
		ChangedFields: map[string]any{"delta": map[string]any{"score": machine}},
	})
}

// leadBehavioralSignals derives the §3.1 signal rows from the lead's
// linked activities: an inbound email is a reply, a meeting counts by
// its recorded status.
func leadBehavioralSignals(ctx context.Context, tx pgx.Tx, leadID ids.LeadID) ([]BehavioralSignal, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.kind, coalesce(a.direction, ''), coalesce(a.meeting_status, ''), a.occurred_at
		FROM activity a
		JOIN activity_link l ON l.activity_id = a.id
		WHERE l.lead_id = $1 AND a.archived_at IS NULL
		ORDER BY a.occurred_at, a.id`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var signals []BehavioralSignal
	for rows.Next() {
		var id ids.ActivityID
		var kind, direction, meetingStatus string
		var occurredAt time.Time
		if err := rows.Scan(&id, &kind, &direction, &meetingStatus, &occurredAt); err != nil {
			return nil, err
		}
		var signalKind string
		switch {
		case kind == "email" && direction == "inbound":
			signalKind = "reply"
		case kind == "meeting" && meetingStatus == "held":
			signalKind = "meeting_held"
		case kind == "meeting" && meetingStatus == "booked":
			signalKind = "meeting_booked"
		default:
			continue
		}
		signals = append(signals, BehavioralSignal{Kind: signalKind, OccurredAt: occurredAt, ActivityID: id})
	}
	return signals, rows.Err()
}

// LeadScoreWorkflows returns the system handlers the engine runs on
// every activity event; compose registers them via
// RegisterSystemWorkflow (always on, not catalog automations).
func LeadScoreWorkflows(store *Store) []workflow.Handler {
	return []workflow.Handler{
		leadScoreRecompute{store: store, name: "recompute_lead_score", trigger: "activity.captured", now: time.Now},
		leadScoreRecompute{store: store, name: "recompute_lead_score_on_update", trigger: "activity.updated", now: time.Now},
	}
}

type leadScoreRecompute struct {
	store   *Store
	name    string
	trigger string
	now     func() time.Time
}

func (w leadScoreRecompute) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    w.name,
		Trigger: workflow.Trigger{EventType: w.trigger},
		Tier:    mcp.TierGreen,
	}
}

// Match is true for every activity event: whether the activity touches
// a lead is the Apply-side query — the envelope payload does not carry
// links.
func (leadScoreRecompute) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (w leadScoreRecompute) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionRecomputeScore, Target: ev.Entity,
	}}}, nil
}

func (w leadScoreRecompute) Apply(ctx context.Context, ev workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	leads, err := w.linkedLeads(ctx, ids.From[ids.ActivityKind](ev.Entity.ID))
	if err != nil {
		return workflow.RunResult{}, err
	}
	now := w.now().UTC()
	for _, leadID := range leads {
		if err := w.store.RecomputeLeadScore(ctx, leadID, now); err != nil {
			return workflow.RunResult{}, fmt.Errorf("recompute lead %s: %w", leadID, err)
		}
	}
	if len(leads) == 0 {
		return workflow.RunResult{}, nil
	}
	return workflow.RunResult{Applied: eff.Actions}, nil
}

func (w leadScoreRecompute) IdempotencyKey(ev workflow.Event) string {
	return w.name + ":" + ev.ID.String()
}

// linkedLeads answers which leads the activity touches — usually none.
func (w leadScoreRecompute) linkedLeads(ctx context.Context, activityID ids.ActivityID) ([]ids.LeadID, error) {
	var leads []ids.LeadID
	err := w.store.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT lead_id FROM activity_link WHERE activity_id = $1 AND lead_id IS NOT NULL`, activityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.LeadID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			leads = append(leads, id)
		}
		return rows.Err()
	})
	return leads, err
}
