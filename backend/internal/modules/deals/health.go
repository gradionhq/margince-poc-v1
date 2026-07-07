// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Deal health (formulas-and-rules §10.5, B-E09.15/.16/.17): one
// deterministic weighted sum of four normalized factors, so a golden
// test can assert health == Σ wᵢ·factorᵢ and every factor carries its
// source-record evidence (P6 "no mystery number"). This is a *health*
// lens, not the Morning-Brief priority composite. Computing it is
// advisory by construction: the store path is read-only — it never
// writes the score back, flips a flag, or touches the deal row.

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// §10.5 tunables (spec parameter-registry names in comments).
const (
	healthWeightActivity    = 0.30 // W_HEALTH_ACT
	healthWeightVelocity    = 0.25 // W_HEALTH_VEL
	healthWeightEngagement  = 0.20 // W_HEALTH_ENG
	healthWeightCommitments = 0.25 // W_HEALTH_COM

	// engageNorm is the distinct engaged-stakeholder count at which the
	// engagement factor saturates.
	engageNorm = 3.0 // ENGAGE_NORM

	// stageVelocityFallbackDays stands in for the workspace median when
	// fewer than stageVelocityMinWonDeals won deals carry history for
	// the subject's current stage.
	stageVelocityFallbackDays = 14.0 // STAGE_VELOCITY_FALLBACK_DAYS
	stageVelocityMinWonDeals  = 10

	// dealHealthAtRisk is the at-risk threshold: health below it drives
	// the §11/§8 at-risk flag and signal.kind='risk'.
	dealHealthAtRisk = 0.35 // DEAL_HEALTH_AT_RISK

	// healthEngagementWindowDays is the §4 two-way-engagement window an
	// "engaged" stakeholder must have both directions inside.
	healthEngagementWindowDays = 90
)

// DealHealthFactors is the per-factor decomposition, each 0..1
// (1.0 = healthiest).
type DealHealthFactors struct {
	ActivityRecency float64
	StageVelocity   float64
	Engagement      float64
	Commitments     float64
}

// DealHealthWeights exposes the weights the score folded with, so a
// client can reconcile health == Σ wᵢ·factorᵢ without knowing the spec.
type DealHealthWeights struct {
	ActivityRecency float64
	StageVelocity   float64
	Engagement      float64
	Commitments     float64
}

// DealHealthEvidence links every factor to the records it was computed
// from — the clickable "why" behind each number.
type DealHealthEvidence struct {
	// MostRecentActivityID backs the recency factor; nil when the deal
	// has never seen an activity (recency 0.0 by definition).
	// note: the health evidence ids (activity/person refs below) are
	// operational explainability pointers gathered through the generic
	// collectIDs helper, not typed entity handles — they stay ids.UUID.
	MostRecentActivityID *ids.UUID

	// StageVelocity: where the deal sits and how its pace compares.
	CurrentStageID      ids.StageID
	DaysInStage         float64
	ExpectedDaysInStage float64

	// EngagedStakeholderIDs are the person ids counted by the
	// engagement factor (two-way contact inside the window).
	EngagedStakeholderIDs []ids.UUID

	// Commitments: the open overdue task activity ids, and the §8
	// stalled flag that zeroes the factor outright.
	OverdueTaskIDs []ids.UUID
	Stalled        bool
}

// DealHealth is the explainable §10.5 output.
type DealHealth struct {
	DealID   ids.DealID
	Health   float64
	AtRisk   bool
	Factors  DealHealthFactors
	Weights  DealHealthWeights
	Evidence DealHealthEvidence
}

// dealHealthInputs are the raw facts the store gathers; healthFromInputs
// folds them deterministically so the formula is testable without a
// database.
type dealHealthInputs struct {
	dealID         ids.DealID
	status         string
	createdAt      time.Time
	lastActivityAt *time.Time
	waitUntil      *time.Time
	stageID        ids.StageID

	// stageEnteredAt is the latest history entry into the current stage,
	// falling back to createdAt when the deal never moved.
	stageEnteredAt time.Time

	// medianWonStageDays is the workspace median days won deals of this
	// pipeline spent in the subject's current stage; nil below the
	// stageVelocityMinWonDeals history floor (→ fallback).
	medianWonStageDays *float64

	mostRecentActivityID  *ids.UUID
	engagedStakeholderIDs []ids.UUID
	overdueTaskIDs        []ids.UUID
}

// healthFromInputs is the pure §10.5 fold: same inputs + clock, same
// score, no I/O.
func healthFromInputs(in dealHealthInputs, now time.Time) DealHealth {
	expected := stageVelocityFallbackDays
	// A non-positive median (instant stage hops) would make the pace
	// ratio meaningless or divide by zero; the fallback is the honest
	// expectation in that degenerate history too.
	if in.medianWonStageDays != nil && *in.medianWonStageDays > 0 {
		expected = *in.medianWonStageDays
	}
	age := daysBetween(in.stageEnteredAt, now)

	stalled := IsStalled(in.status, in.createdAt, in.lastActivityAt, in.waitUntil, now)
	factors := DealHealthFactors{
		ActivityRecency: recencyScore(in.lastActivityAt, now),
		StageVelocity:   velocityScore(age, expected),
		Engagement:      math.Min(1.0, float64(len(in.engagedStakeholderIDs))/engageNorm),
		Commitments:     commitmentScore(stalled, len(in.overdueTaskIDs)),
	}
	health := healthWeightActivity*factors.ActivityRecency +
		healthWeightVelocity*factors.StageVelocity +
		healthWeightEngagement*factors.Engagement +
		healthWeightCommitments*factors.Commitments

	return DealHealth{
		DealID:  in.dealID,
		Health:  health,
		AtRisk:  health < dealHealthAtRisk,
		Factors: factors,
		Weights: DealHealthWeights{
			ActivityRecency: healthWeightActivity,
			StageVelocity:   healthWeightVelocity,
			Engagement:      healthWeightEngagement,
			Commitments:     healthWeightCommitments,
		},
		Evidence: DealHealthEvidence{
			MostRecentActivityID:  in.mostRecentActivityID,
			CurrentStageID:        in.stageID,
			DaysInStage:           age,
			ExpectedDaysInStage:   expected,
			EngagedStakeholderIDs: in.engagedStakeholderIDs,
			OverdueTaskIDs:        in.overdueTaskIDs,
			Stalled:               stalled,
		},
	}
}

// recencyScore is the §10.5 recency band over last_activity_at: an
// absolute-duration day count on UTC instants (like IsStalled), never a
// calendar-day count.
func recencyScore(lastActivityAt *time.Time, now time.Time) float64 {
	if lastActivityAt == nil {
		return 0.0
	}
	d := daysBetween(*lastActivityAt, now)
	switch {
	case d <= 3:
		return 1.0
	case d <= 7:
		return 0.8
	case d <= 14:
		return 0.6
	case d <= 30:
		return 0.4
	case d <= 60:
		return 0.2
	default:
		return 0.0
	}
}

// velocityScore bands the pace ratio r = age/expected: at or ahead of
// the won-deal median is healthy, ≥2× expected is stuck.
func velocityScore(ageDays, expectedDays float64) float64 {
	r := ageDays / expectedDays
	switch {
	case r <= 1.0:
		return 1.0
	case r <= 1.5:
		return 0.6
	case r <= 2.0:
		return 0.3
	default:
		return 0.0
	}
}

// commitmentScore: a §8-stalled deal has broken its commitments outright;
// otherwise the open-overdue-task count bands the factor.
func commitmentScore(stalled bool, openOverdue int) float64 {
	if stalled {
		return 0.0
	}
	switch openOverdue {
	case 0:
		return 1.0
	case 1:
		return 0.5
	default:
		return 0.2
	}
}

// daysBetween is the fractional UTC-instant day count, floored at zero
// so a fixed test clock behind a freshly seeded row cannot go negative.
func daysBetween(from, to time.Time) float64 {
	d := to.Sub(from).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}

// healthActivityKinds are the qualifying two-way-engagement interaction
// kinds (§4 inputs; tasks and notes are not contact).
const healthActivityKinds = `('email','call','meeting')`

// DealHealth computes the §10.5 score for one deal. The read is
// row-scoped exactly like GetDeal — a deal the caller cannot see has no
// health to disclose — and the whole path is read-only (B-E09.17): the
// transaction issues SELECTs only.
func (s *Store) DealHealth(ctx context.Context, dealID ids.DealID, now time.Time) (DealHealth, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return DealHealth{}, err
	}
	in := dealHealthInputs{dealID: dealID}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", dealID.UUID); err != nil {
			return err
		}
		return healthInputs(ctx, tx, now, &in)
	})
	if err != nil {
		return DealHealth{}, err
	}
	return healthFromInputs(in, now), nil
}

// healthInputs gathers the raw facts for one deal inside the workspace
// transaction: the deal row, its stage-entry instant, the won-deal
// stage-duration median, and the per-factor evidence rows.
func healthInputs(ctx context.Context, tx pgx.Tx, now time.Time, in *dealHealthInputs) error {
	var pipelineID ids.PipelineID
	err := tx.QueryRow(ctx, `
		SELECT status, created_at, last_activity_at, wait_until, stage_id, pipeline_id
		FROM deal WHERE id = $1 AND archived_at IS NULL`, in.dealID).
		Scan(&in.status, &in.createdAt, &in.lastActivityAt, &in.waitUntil, &in.stageID, &pipelineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return err
	}

	// Days in current stage: the latest entry INTO this stage; a deal
	// that never moved has sat there since creation.
	var enteredAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT max(changed_at) FROM deal_stage_history
		WHERE deal_id = $1 AND to_stage_id = $2`, in.dealID, in.stageID).Scan(&enteredAt); err != nil {
		return err
	}
	in.stageEnteredAt = in.createdAt
	if enteredAt != nil {
		in.stageEnteredAt = *enteredAt
	}

	// Expected pace: for each won deal of this pipeline, the completed
	// stints in the subject's current stage (entry row → the next stage
	// change); the median of those durations. A stint still open at the
	// win has no exit row and contributes nothing. Below the
	// ten-won-deal history floor the median is statistically noise, so
	// the fold falls back to STAGE_VELOCITY_FALLBACK_DAYS.
	var wonDealsWithHistory int
	var medianSeconds *float64
	if err := tx.QueryRow(ctx, `
		WITH stints AS (
			SELECT h.deal_id, h.to_stage_id, h.changed_at AS entered,
			       lead(h.changed_at) OVER (PARTITION BY h.deal_id ORDER BY h.changed_at, h.id) AS left_at
			FROM deal_stage_history h
			JOIN deal d ON d.id = h.deal_id
			WHERE d.pipeline_id = $1 AND d.status = 'won' AND d.archived_at IS NULL
		)
		SELECT count(DISTINCT deal_id),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM left_at - entered))
		FROM stints
		WHERE to_stage_id = $2 AND left_at IS NOT NULL`,
		pipelineID, in.stageID).Scan(&wonDealsWithHistory, &medianSeconds); err != nil {
		return err
	}
	if wonDealsWithHistory >= stageVelocityMinWonDeals && medianSeconds != nil {
		days := *medianSeconds / 86400
		in.medianWonStageDays = &days
	}

	// Recency evidence: the freshest live activity on the deal — the
	// record behind deal.last_activity_at.
	var recent ids.UUID
	err = tx.QueryRow(ctx, `
		SELECT a.id FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = $1
		WHERE a.archived_at IS NULL
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT 1`, in.dealID).Scan(&recent)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No activity ever: recency is 0.0 with no record to point at.
	case err != nil:
		return err
	default:
		in.mostRecentActivityID = &recent
	}

	// Engagement: live deal_stakeholder persons with BOTH an inbound and
	// an outbound qualifying interaction inside the window — the §4
	// "reciprocity > 0" two-way reading; a one-way broadcast target is
	// not engaged.
	windowStart := now.AddDate(0, 0, -healthEngagementWindowDays)
	engaged, err := collectIDs(tx.Query(ctx, `
		SELECT DISTINCT r.person_id FROM relationship r
		WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $1 AND r.archived_at IS NULL
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN `+healthActivityKinds+` AND a.archived_at IS NULL
			  AND a.occurred_at >= $2 AND a.direction = 'inbound')
		  AND EXISTS (
			SELECT 1 FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.person_id = r.person_id
			WHERE a.kind IN `+healthActivityKinds+` AND a.archived_at IS NULL
			  AND a.occurred_at >= $2 AND a.direction = 'outbound')
		ORDER BY r.person_id`, in.dealID, windowStart))
	if err != nil {
		return err
	}
	in.engagedStakeholderIDs = engaged

	// Commitments evidence: the open overdue tasks on the deal.
	overdue, err := collectIDs(tx.Query(ctx, `
		SELECT a.id FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = $1
		WHERE a.kind = 'task' AND a.is_done = false AND a.archived_at IS NULL
		  AND a.due_at IS NOT NULL AND a.due_at < $2
		ORDER BY a.due_at, a.id`, in.dealID, now))
	if err != nil {
		return err
	}
	in.overdueTaskIDs = overdue
	return nil
}

// collectIDs drains a single-uuid-column result set.
func collectIDs(rows pgx.Rows, err error) ([]ids.UUID, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
