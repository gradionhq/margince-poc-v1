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
		return recomputeLeadScoreTx(ctx, tx, leadID, now, false)
	})
}

// recomputeLeadScoreTx is the §3 recompute inside an open transaction —
// shared by the SYSTEM workflow lane and the override-clear path in
// UpdateLead. When a Commercial Judgement override is in force (a
// non-empty score_override_reason) it NEVER overwrites lead.score: the
// human value is sticky (formulas §3.1, AC-S1) and the freshly machine
// value is retained in score_computed instead. With no override, score
// tracks the machine value directly (score_computed stays null).
// clearedOverride tells this recompute it is the one a withdrawn
// Commercial Judgement override triggered — the single case where an
// unmoved score still has to be recorded (see below).
func recomputeLeadScoreTx(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, now time.Time, clearedOverride bool) error {
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
	// A rep's own inputs count toward the same weighted total — that is the
	// point of supplying them — and stay their own labelled rows.
	manual, err := leadManualFactors(ctx, tx, leadID)
	if err != nil {
		return err
	}
	intents, err := loadSourceIntents(ctx, tx)
	if err != nil {
		return err
	}
	scored := ScoreLeadDetail(deref(title), intents.Of(deref(source)), signals, now).withManual(manual)
	machine := scored.Score

	// Sticky override: the machine value moves score_computed, never score.
	if overrideReason != nil {
		return recomputeUnderOverrideTx(ctx, tx, leadID, overrideUpdate{
			displayed: currentScore, previousComputed: currentComputed,
			reason: overrideReason, scored: scored,
		})
	}

	// Unchanged score, no entry: history records the number MOVING, which is
	// what a trend plots and what a rep asks about. Appending on every decay
	// tick would bury that under drift nobody reads (ADR-0105 §5).
	//
	// The exception is the recompute that a CLEARED override triggers. That
	// clear has already set score back to the retained machine value, so the
	// number has not moved by the time this runs — yet the newest entry still
	// carries the override's reason and its two divergent numbers. Left out,
	// the series would say a withdrawn override is still in force.
	if machine == currentScore {
		if !clearedOverride {
			return nil
		}
		return appendLeadScoreHistory(ctx, tx, leadID, machine, scored, nil)
	}
	// Same CAS, same reason: machine was computed from the score this run
	// read, so a concurrent recompute that already moved it wins.
	moved, err := tx.Exec(ctx,
		`UPDATE lead SET score = $2 WHERE id = $1 AND score = $3`, leadID, machine, currentScore)
	if err != nil {
		return err
	}
	if moved.RowsAffected() == 0 {
		return nil
	}
	if err := appendLeadScoreHistory(ctx, tx, leadID, machine, scored, nil); err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
		map[string]any{"score": currentScore}, map[string]any{"score": machine})
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
		ChangedFields: map[string]any{"delta": map[string]any{"score": machine}},
	})
}

// fieldKeyScoreComputed names the machine score on the audit trail and
// the event delta, which must spell it identically.
const fieldKeyScoreComputed = "score_computed"

// overrideUpdate carries what the override arm needs to record one
// recompute that ran while a human's number was in force.
type overrideUpdate struct {
	displayed        int     // the human's score, which this path never moves
	previousComputed *int    // the machine value before this run, for the audit delta
	reason           *string // the written reason keeping the override alive
	scored           LeadScoring
}

// recomputeUnderOverrideTx refreshes the machine value beside a Commercial
// Judgement override without disturbing the score on screen (A68/ADR-0053).
func recomputeUnderOverrideTx(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, in overrideUpdate) error {
	machine := in.scored.Score
	if in.previousComputed != nil && *in.previousComputed == machine {
		return nil
	}
	// CAS on the value this run read: the machine score was derived from a
	// pre-read of score_computed, so a concurrent recompute that already
	// moved it must not be overwritten with a number computed from the
	// state before it landed. A lost race writes nothing and appends no
	// entry, which is correct — the winner recorded the same recompute.
	tag, err := tx.Exec(ctx,
		`UPDATE lead SET score_computed = $2 WHERE id = $1 AND score_computed IS NOT DISTINCT FROM $3`,
		leadID, machine, in.previousComputed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	// The displayed score stays the human's; the factors explain the
	// machine's. The entry carries both so the read can say which is which
	// instead of presenting one as an account of the other.
	if err := appendLeadScoreHistory(ctx, tx, leadID, in.displayed, in.scored, in.reason); err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
		map[string]any{fieldKeyScoreComputed: in.previousComputed}, map[string]any{fieldKeyScoreComputed: machine})
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
		ChangedFields: map[string]any{eventKeyDelta: map[string]any{fieldKeyScoreComputed: machine}},
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
		WHERE l.lead_id = $1 AND a.archived_at IS NULL AND `+auth.ActivityAvailableClause("a")+`
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
		Tier:    mcp.TierAutoExecute,
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
