// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"math"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func healthFixedNow() time.Time {
	return time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
}

func daysAgo(days float64) time.Time {
	return healthFixedNow().Add(-time.Duration(days * 24 * float64(time.Hour)))
}

func daysAgoPtr(days float64) *time.Time {
	t := daysAgo(days)
	return &t
}

// healthyInputs is the §10.5 worked-example fixture: last activity 5
// days ago, 1.2× the expected stage pace, two engaged stakeholders, no
// overdue commitments, not stalled.
func healthyInputs() dealHealthInputs {
	median := 14.0
	return dealHealthInputs{
		dealID:                ids.New[ids.DealKind](),
		status:                "open",
		createdAt:             daysAgo(40),
		lastActivityAt:        daysAgoPtr(5),
		stageID:               ids.New[ids.StageKind](),
		stageEnteredAt:        daysAgo(1.2 * 14.0),
		medianWonStageDays:    &median,
		engagedStakeholderIDs: []ids.UUID{ids.NewV7(), ids.NewV7()},
	}
}

// The spec's worked example (§10.5): recency 0.8, velocity 0.6,
// engagement 2/3, commitments 1.0. The prose prints 0.774 by rounding
// engagement to 0.67; the exact fold is 0.30·0.8 + 0.25·0.6 + 0.20·⅔ +
// 0.25·1.0 and the golden value pins THAT — the score must reproduce
// bit-for-bit, not to display precision.
func TestDealHealthReproducesTheWorkedExample(t *testing.T) {
	got := healthFromInputs(healthyInputs(), healthFixedNow())

	want := DealHealthFactors{ActivityRecency: 0.8, StageVelocity: 0.6, Engagement: 2.0 / 3.0, Commitments: 1.0}
	if got.Factors != want {
		t.Fatalf("factors = %+v, want %+v", got.Factors, want)
	}
	wantHealth := 0.30*0.8 + 0.25*0.6 + 0.20*(2.0/3.0) + 0.25*1.0
	if math.Abs(got.Health-wantHealth) > 1e-9 {
		t.Fatalf("health = %.12f, want %.12f", got.Health, wantHealth)
	}
	if got.AtRisk {
		t.Fatalf("the worked example is healthy, not at risk (health %.3f)", got.Health)
	}
	if got.Evidence.Stalled {
		t.Fatal("the worked example is not stalled")
	}
}

// The golden decomposition (B-E09.16, "no mystery number"): the score
// reconciles exactly to the exposed factors folded with the exposed
// weights — no hidden term.
func TestDealHealthDecomposesToItsWeightedFactors(t *testing.T) {
	in := healthyInputs()
	in.lastActivityAt = daysAgoPtr(20)
	in.overdueTaskIDs = []ids.UUID{ids.NewV7()}
	got := healthFromInputs(in, healthFixedNow())

	recomposed := got.Weights.ActivityRecency*got.Factors.ActivityRecency +
		got.Weights.StageVelocity*got.Factors.StageVelocity +
		got.Weights.Engagement*got.Factors.Engagement +
		got.Weights.Commitments*got.Factors.Commitments
	if got.Health != recomposed {
		t.Fatalf("health %.12f does not reconcile to Σ wᵢ·factorᵢ = %.12f", got.Health, recomposed)
	}
	if sum := got.Weights.ActivityRecency + got.Weights.StageVelocity + got.Weights.Engagement + got.Weights.Commitments; sum != 1.0 {
		t.Fatalf("weights sum to %f, want 1.0", sum)
	}
}

// A deal parked in an early stage with no contact at all: recency 0,
// velocity 0 (way past expected), engagement 0, commitments 0 (stalled)
// → health 0, flagged at-risk.
func TestDealHealthFlagsTheSilentParkedDealAtRisk(t *testing.T) {
	in := dealHealthInputs{
		dealID:         ids.New[ids.DealKind](),
		status:         "open",
		createdAt:      daysAgo(90),
		stageID:        ids.New[ids.StageKind](),
		stageEnteredAt: daysAgo(90),
	}
	got := healthFromInputs(in, healthFixedNow())
	if got.Health >= dealHealthAtRisk || !got.AtRisk {
		t.Fatalf("silent parked deal → health %.3f (at_risk=%v), want < %.2f and at_risk", got.Health, got.AtRisk, dealHealthAtRisk)
	}
	if !got.Evidence.Stalled {
		t.Fatal("90 idle days must read as stalled (§8)")
	}
}

func TestRecencyScoreBandEdges(t *testing.T) {
	now := healthFixedNow()
	if got := recencyScore(nil, now); got != 0.0 {
		t.Errorf("no activity ever → %f, want 0.0", got)
	}
	for _, tc := range []struct {
		days float64
		want float64
	}{
		{0, 1.0}, {3, 1.0}, {3.001, 0.8}, {7, 0.8}, {7.001, 0.6}, {14, 0.6},
		{14.001, 0.4}, {30, 0.4}, {30.001, 0.2}, {60, 0.2}, {60.001, 0.0},
	} {
		if got := recencyScore(daysAgoPtr(tc.days), now); got != tc.want {
			t.Errorf("recency at %g days = %f, want %f", tc.days, got, tc.want)
		}
	}
}

func TestVelocityScoreBands(t *testing.T) {
	for _, tc := range []struct {
		age, expected float64
		want          float64
	}{
		{0, 14, 1.0}, {14, 14, 1.0}, {14.001, 14, 0.6}, {21, 14, 0.6},
		{21.001, 14, 0.3}, {28, 14, 0.3}, {28.001, 14, 0.0},
	} {
		if got := velocityScore(tc.age, tc.expected); got != tc.want {
			t.Errorf("velocity at age %g / expected %g = %f, want %f", tc.age, tc.expected, got, tc.want)
		}
	}
}

func TestCommitmentScoreBands(t *testing.T) {
	for overdue, want := range map[int]float64{0: 1.0, 1: 0.5, 2: 0.2, 5: 0.2} {
		if got := commitmentScore(false, overdue); got != want {
			t.Errorf("%d overdue → %f, want %f", overdue, got, want)
		}
	}
	// Stalled zeroes the factor regardless of the overdue count.
	for _, overdue := range []int{0, 1, 3} {
		if got := commitmentScore(true, overdue); got != 0.0 {
			t.Errorf("stalled with %d overdue → %f, want 0.0", overdue, got)
		}
	}
}

// The engagement factor saturates at ENGAGE_NORM distinct engaged
// stakeholders and never exceeds 1.0.
func TestEngagementSaturatesAtTheNorm(t *testing.T) {
	in := healthyInputs()
	for count, want := range map[int]float64{0: 0.0, 1: 1.0 / 3.0, 3: 1.0, 5: 1.0} {
		in.engagedStakeholderIDs = make([]ids.UUID, count)
		got := healthFromInputs(in, healthFixedNow())
		if math.Abs(got.Factors.Engagement-want) > 1e-9 {
			t.Errorf("%d engaged stakeholders → %f, want %f", count, got.Factors.Engagement, want)
		}
	}
}

// Below the ten-won-deal history floor (medianWonStageDays nil) the
// expected pace is STAGE_VELOCITY_FALLBACK_DAYS; a degenerate zero
// median (instant stage hops) falls back too instead of dividing by it.
func TestVelocityFallsBackWithoutEnoughWonHistory(t *testing.T) {
	in := healthyInputs()
	in.medianWonStageDays = nil
	got := healthFromInputs(in, healthFixedNow())
	if got.Evidence.ExpectedDaysInStage != stageVelocityFallbackDays {
		t.Fatalf("expected days = %f, want the %g-day fallback", got.Evidence.ExpectedDaysInStage, float64(stageVelocityFallbackDays))
	}
	if got.Factors.StageVelocity != 0.6 {
		t.Fatalf("16.8 days against the 14-day fallback is 1.2× → 0.6, got %f", got.Factors.StageVelocity)
	}

	zero := 0.0
	in.medianWonStageDays = &zero
	if got := healthFromInputs(in, healthFixedNow()); got.Evidence.ExpectedDaysInStage != stageVelocityFallbackDays {
		t.Fatalf("zero median must fall back, got expected days %f", got.Evidence.ExpectedDaysInStage)
	}
}
