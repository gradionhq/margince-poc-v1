// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The §3 golden tests: the spec's worked example reproduces exactly,
// the breakdown sums to the score (AC-S1), and recompute under a fixed
// clock is idempotent (AC-S2/S3).

import (
	"math"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestScoreLeadWorkedExample(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	signals := []BehavioralSignal{
		{Kind: "reply", OccurredAt: now.AddDate(0, 0, -2), ActivityID: ids.New[ids.ActivityKind]()},
		{Kind: "link_click", OccurredAt: now.AddDate(0, 0, -10), ActivityID: ids.New[ids.ActivityKind]()},
		{Kind: "link_click", OccurredAt: now.AddDate(0, 0, -10), ActivityID: ids.New[ids.ActivityKind]()},
	}
	score, factors := scoreLeadBySource("VP Sales", "webform", signals, now)
	if score != 51 {
		t.Fatalf("worked example score = %d, want 51 (factors: %+v)", score, factors)
	}
	var sum float64
	for _, f := range factors {
		sum += f.Points
	}
	if int(math.Floor(sum+0.5)) != score {
		t.Fatalf("breakdown sums to %.2f but score is %d — Explain This Score must reconcile", sum, score)
	}
	// Idempotent under the fixed clock.
	again, _ := scoreLeadBySource("VP Sales", "webform", signals, now)
	if again != score {
		t.Fatalf("recompute drifted: %d → %d", score, again)
	}
}

func TestScoreLeadEdges(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	if score, _ := scoreLeadBySource("Intern", "crawl", nil, now); score != 0 {
		t.Errorf("negative fit must clamp at 0, got %d", score)
	}
	if score, _ := scoreLeadBySource("CEO", "referral", nil, now); score != 23 {
		t.Errorf("pure-fit cold lead = %d, want 23", score)
	}
	// Unknown signal kinds contribute nothing (column-readiness rule).
	score, factors := scoreLeadBySource("", "manual", []BehavioralSignal{
		{Kind: "engagement_event_not_yet_shipped", OccurredAt: now},
	}, now)
	if score != 0 || len(factors) != 0 {
		t.Errorf("unknown signal leaked into the score: %d %+v", score, factors)
	}
	// A flood of replies clamps at the max.
	var flood []BehavioralSignal
	for range 10 {
		flood = append(flood, BehavioralSignal{Kind: "reply", OccurredAt: now})
	}
	if score, _ := scoreLeadBySource("CTO", "inbound", flood, now); score != 100 {
		t.Errorf("clamp ceiling: %d, want 100", score)
	}
}

// TestScoreLeadProductionPathFixture pins what the SAME lead as the worked
// example scores through the path the product actually runs (ADR-0105 §6).
//
// The formula fixture above proves the arithmetic including link_click,
// which is the only coverage of a weight A3 ratified. It cannot also stand
// for what a rep sees, because clicks never reach the scorer: ingestion
// materializes replies and meetings only, and email_open is dead by the
// honest-signal ruling. Both numbers are true about different things, so
// both are pinned — replacing 51 with 46 would delete the click weight's
// only test until the day it starts firing.
func TestScoreLeadProductionPathFixture(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	// The worked example's lead, carrying only the signals the ingestion
	// path can produce today.
	scored := ScoreLeadDetail("VP Sales", SourceIntentHigh, []BehavioralSignal{
		{Kind: "reply", OccurredAt: now.AddDate(0, 0, -2), ActivityID: ids.New[ids.ActivityKind]()},
	}, now)
	if scored.Score != 46 {
		t.Fatalf("production-path score = %d, want 46 (factors: %+v)", scored.Score, scored.Factors)
	}
}

// TestScoreLeadDetailSeparatesRoundingFromClamping is the arithmetic the
// explain response has to be able to state. Collapsing the two steps
// reports a clamp where only rounding happened, and names a clamp source
// that never existed (ADR-0105 §2).
func TestScoreLeadDetailSeparatesRoundingFromClamping(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	// Rounding alone: a decayed reply lands on a fraction, well inside the
	// bounds, so rounded and stored agree and no clamp is implied.
	rounding := ScoreLeadDetail("", SourceIntentHigh, []BehavioralSignal{
		{Kind: "reply", OccurredAt: now.AddDate(0, 0, -3), ActivityID: ids.New[ids.ActivityKind]()},
	}, now)
	if rounding.RawSum == float64(rounding.RoundedSum) {
		t.Fatalf("fixture is not fractional: raw %.4f", rounding.RawSum)
	}
	if rounding.RoundedSum != rounding.Score {
		t.Errorf("no clamp should apply here: rounded %d, stored %d", rounding.RoundedSum, rounding.Score)
	}

	// Clamping: the cap acts on the ROUNDED value, so RoundedSum must
	// exceed the max while the stored score sits at it.
	var flood []BehavioralSignal
	for range 10 {
		flood = append(flood, BehavioralSignal{Kind: "reply", OccurredAt: now, ActivityID: ids.New[ids.ActivityKind]()})
	}
	clamped := ScoreLeadDetail("CTO", SourceIntentHigh, flood, now)
	if clamped.Score != leadScoreMax {
		t.Fatalf("expected the ceiling, got %d", clamped.Score)
	}
	if clamped.RoundedSum <= leadScoreMax {
		t.Errorf("rounded sum %d does not show the clamp it fired", clamped.RoundedSum)
	}
	if clamped.RawSum <= float64(leadScoreMax) {
		t.Errorf("raw sum %.2f should exceed the cap", clamped.RawSum)
	}
}

// TestBehavioralFactorsCarryTheirUndecayedBase is what lets a reader see
// the decay as arithmetic (`raw · 2^(−days/14)`, AC-leads-4/10) instead of
// being handed a number to take on faith.
func TestBehavioralFactorsCarryTheirUndecayedBase(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	scored := ScoreLeadDetail("", SourceIntentNeutral, []BehavioralSignal{
		{Kind: "reply", OccurredAt: now.AddDate(0, 0, -14), ActivityID: ids.New[ids.ActivityKind]()},
	}, now)
	if len(scored.Factors) != 1 {
		t.Fatalf("want one behavioral factor, got %+v", scored.Factors)
	}
	factor := scored.Factors[0]
	if factor.BasePoints != behavioralReplyPoints {
		t.Errorf("base points = %v, want %v", factor.BasePoints, behavioralReplyPoints)
	}
	// One half-life exactly: the decayed value is half the base, which is
	// the claim the rendered arithmetic makes.
	if math.Abs(factor.Points-float64(behavioralReplyPoints)/2) > 0.001 {
		t.Errorf("one half-life should halve the base: got %.4f from %v", factor.Points, factor.BasePoints)
	}
}

// TestWithManualFoldsHumanInputIntoOneTotal proves a rep's own input counts
// toward the same weighted score, and that rounding runs ONCE over the
// whole breakdown rather than over an already-rounded number.
func TestWithManualFoldsHumanInputIntoOneTotal(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	machine := ScoreLeadDetail("VP Sales", SourceIntentHigh, nil, now)
	withManual := machine.withManual([]ScoreFactor{{Factor: "manual:employees", Points: 8}})

	if withManual.Score != machine.Score+8 {
		t.Errorf("manual input did not count: %d → %d", machine.Score, withManual.Score)
	}
	if len(withManual.Factors) != len(machine.Factors)+1 {
		t.Errorf("manual factor is not its own row: %+v", withManual.Factors)
	}
	var sum float64
	for _, f := range withManual.Factors {
		sum += f.Points
	}
	if math.Abs(sum-withManual.RawSum) > 0.001 {
		t.Errorf("raw sum %.4f does not match its own factors (%.4f)", withManual.RawSum, sum)
	}
	// An empty input list must not disturb the run it was folded into.
	if unchanged := machine.withManual(nil); unchanged.Score != machine.Score {
		t.Errorf("empty manual set moved the score: %d → %d", machine.Score, unchanged.Score)
	}
}

// scoreLeadBySource is the test-side spelling of the old source-keyed
// scorer: it resolves the seeded weighting the way production resolves the
// installation's table, so the worked examples still read by source name.
func scoreLeadBySource(title, source string, signals []BehavioralSignal, now time.Time) (int, []ScoreFactor) {
	scored := ScoreLeadDetail(title, defaultSourceIntents.Of(source), signals, now)
	return scored.Score, scored.Factors
}

func TestSourceIntentsResolveConnectorFamilies(t *testing.T) {
	intents := SourceIntents{"connector:apollo": SourceIntentHigh, "import": SourceIntentLow}
	cases := map[string]SourceIntent{
		"connector:apollo:a-1": SourceIntentHigh,
		"connector:apollo":     SourceIntentHigh,
		"connector:other:x":    SourceIntentNeutral,
		"import":               SourceIntentLow,
		"seed":                 SourceIntentNeutral,
		"":                     SourceIntentNeutral,
	}
	for source, want := range cases {
		if got := intents.Of(source); got != want {
			t.Errorf("Of(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestDeriveSourceKey(t *testing.T) {
	cases := map[string]string{"Trade show": "trade_show", "  Web-Form!! ": "web_form", "Ökologisch": "kologisch", "": ""}
	for label, want := range cases {
		if got := deriveSourceKey(label); got != want {
			t.Errorf("deriveSourceKey(%q) = %q, want %q", label, got, want)
		}
	}
}
