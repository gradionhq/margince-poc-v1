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
		{Kind: "reply", OccurredAt: now.AddDate(0, 0, -2), ActivityID: ids.NewV7()},
		{Kind: "link_click", OccurredAt: now.AddDate(0, 0, -10), ActivityID: ids.NewV7()},
		{Kind: "link_click", OccurredAt: now.AddDate(0, 0, -10), ActivityID: ids.NewV7()},
	}
	score, factors := ScoreLead("VP Sales", "webform", signals, now)
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
	again, _ := ScoreLead("VP Sales", "webform", signals, now)
	if again != score {
		t.Fatalf("recompute drifted: %d → %d", score, again)
	}
}

func TestScoreLeadEdges(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	if score, _ := ScoreLead("Intern", "crawl", nil, now); score != 0 {
		t.Errorf("negative fit must clamp at 0, got %d", score)
	}
	if score, _ := ScoreLead("CEO", "referral", nil, now); score != 23 {
		t.Errorf("pure-fit cold lead = %d, want 23", score)
	}
	// Unknown signal kinds contribute nothing (column-readiness rule).
	score, factors := ScoreLead("", "manual", []BehavioralSignal{
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
	if score, _ := ScoreLead("CTO", "inbound", flood, now); score != 100 {
		t.Errorf("clamp ceiling: %d, want 100", score)
	}
}
