// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pure §10.1 fold (B-E05.1) against the spec's own worked examples,
// the timing buckets, the stable tie-break order, the honest-short
// cutoff, and the B-E05.12 evidence-or-omit gate — all without a
// database: same facts + clock must always fold to the same queue.

import (
	"math"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// briefTestClock is the fixed evaluation instant of every fold below.
var briefTestClock = time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

func datePtr(t time.Time) *time.Time { return &t }

func int64Ptr(v int64) *int64 { return &v }

// closeIn returns a date the given whole days after the test clock's day.
func closeIn(days int) *time.Time {
	return datePtr(briefTestClock.UTC().Truncate(24*time.Hour).AddDate(0, 0, days))
}

func uuidAt(b byte) ids.UUID {
	var u ids.UUID
	u[15] = b
	return u
}

// The three §10.1 worked examples, folded at a P90 of €80,000 minor.
func workedExampleFacts() (a, b, c briefDealFacts) {
	a = briefDealFacts{
		dealID:               uuidAt(1),
		winProbability:       80,
		baseValueMinor:       int64Ptr(60_000_00),
		expectedClose:        closeIn(5),
		overnightActivityIDs: []ids.UUID{uuidAt(101)},
		warmthStrength:       47,
		warmthEvidence:       []ids.UUID{uuidAt(102)},
	}
	b = briefDealFacts{
		dealID:         uuidAt(2),
		winProbability: 25,
		expectedClose:  closeIn(200),
		warmthStrength: 10,
		warmthEvidence: []ids.UUID{uuidAt(103)},
	}
	c = briefDealFacts{
		dealID:         uuidAt(3),
		winProbability: 10,
		expectedClose:  closeIn(200),
	}
	return a, b, c
}

func TestBriefCompositeReproducesTheSpecWorkedExamples(t *testing.T) {
	const workedExampleNorm = 80_000_00
	a, b, c := workedExampleFacts()

	itemA := briefScore(a, workedExampleNorm, briefTestClock)
	wantA := 0.30*0.80 + 0.25*0.75 + 0.20*1.0 + 0.15*1.0 + 0.10*0.47
	if math.Abs(itemA.Composite-wantA) > 1e-9 {
		t.Fatalf("Deal A composite = %.6f, want %.6f", itemA.Composite, wantA)
	}
	// The spec prints the sum rounded to three decimals as 0.825.
	if math.Round(itemA.Composite*1000)/1000 != 0.825 {
		t.Fatalf("Deal A composite %.6f does not round to the spec's 0.825", itemA.Composite)
	}
	wantFeatures := BriefFeatureVector{Winnability: 0.80, Revenue: 0.75, Timing: 1.0, Momentum: 1.0, Warmth: 0.47}
	if itemA.Features != wantFeatures {
		t.Fatalf("Deal A features = %+v, want %+v", itemA.Features, wantFeatures)
	}

	itemB := briefScore(b, workedExampleNorm, briefTestClock)
	if math.Abs(itemB.Composite-0.185) > 1e-9 {
		t.Fatalf("Deal B composite = %.6f, want 0.185", itemB.Composite)
	}

	itemC := briefScore(c, workedExampleNorm, briefTestClock)
	if math.Abs(itemC.Composite-0.13) > 1e-9 {
		t.Fatalf("Deal C composite = %.6f, want 0.13", itemC.Composite)
	}

	facts := map[ids.UUID]briefDealFacts{a.dealID: a, b.dealID: b, c.dealID: c}
	queue, candidates := briefQueue([]BriefQueueItem{itemB, itemC, itemA}, facts)
	if candidates != 2 {
		t.Fatalf("candidate count = %d, want 2 (Deal C sits below the 0.15 bar)", candidates)
	}
	if len(queue) != 2 || queue[0].DealID != a.dealID || queue[1].DealID != b.dealID {
		t.Fatalf("queue = %v, want [Deal A, Deal B] with Deal C excluded", queueDeals(queue))
	}
}

func TestBriefTimingBuckets(t *testing.T) {
	cases := []struct {
		name  string
		close *time.Time
		want  float64
	}{
		{"no close date", nil, 0.3},
		{"overdue", closeIn(-1), 0.9},
		{"today", closeIn(0), 1.0},
		{"this week", closeIn(7), 1.0},
		{"this month", closeIn(30), 0.7},
		{"this quarter", closeIn(90), 0.4},
		{"beyond the quarter", closeIn(91), 0.2},
	}
	for _, tc := range cases {
		if got := briefTimingScore(tc.close, briefTestClock); got != tc.want {
			t.Errorf("%s: timing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Unevidenced factors sit at their floors: momentum stays at 0.4 with no
// overnight rows, and a warmth strength with no §4 contributing
// interactions contributes nothing — the score never asserts what it
// cannot show (B-E05.12).
func TestBriefFactorsFloorWithoutEvidence(t *testing.T) {
	f := briefDealFacts{dealID: uuidAt(9), winProbability: 50, warmthStrength: 80}
	item := briefScore(f, briefRevenueNormFallbackMinor, briefTestClock)
	if item.Features.Momentum != briefMomentumUnchanged {
		t.Errorf("momentum without overnight evidence = %v, want the %v floor", item.Features.Momentum, briefMomentumUnchanged)
	}
	if item.Features.Warmth != 0 {
		t.Errorf("warmth without contributing interactions = %v, want 0", item.Features.Warmth)
	}
	if item.Features.Revenue != 0 {
		t.Errorf("revenue without an amount = %v, want 0", item.Features.Revenue)
	}
	if len(item.EvidenceIDs) != 1 || item.EvidenceIDs[0] != f.dealID {
		t.Errorf("evidence = %v, want exactly the deal row", item.EvidenceIDs)
	}
}

// Ties break by higher base value, then sooner expected close, then
// lowest deal id — a stable order under every fixed clock.
func TestBriefTieBreaksAreStable(t *testing.T) {
	// All five deals fold to the identical composite: revenue capped at
	// 1.0 (every base value ≥ the €50k norm) and timing inside the same
	// ≤7-day bucket — only the tie-break inputs differ.
	base := briefDealFacts{winProbability: 60}
	richer, sooner, lowestID, higherID, poorer := base, base, base, base, base

	richer.dealID = uuidAt(4)
	richer.baseValueMinor = int64Ptr(200_000_00)
	richer.expectedClose = closeIn(5)

	sooner.dealID = uuidAt(3)
	sooner.baseValueMinor = int64Ptr(100_000_00)
	sooner.expectedClose = closeIn(3)

	lowestID.dealID = uuidAt(2)
	lowestID.baseValueMinor = int64Ptr(100_000_00)
	lowestID.expectedClose = closeIn(5)

	higherID.dealID = uuidAt(5)
	higherID.baseValueMinor = int64Ptr(100_000_00)
	higherID.expectedClose = closeIn(5)

	poorer.dealID = uuidAt(1)
	poorer.baseValueMinor = int64Ptr(50_000_00)
	poorer.expectedClose = closeIn(5)

	facts := map[ids.UUID]briefDealFacts{}
	var scored []BriefQueueItem
	for _, f := range []briefDealFacts{poorer, richer, higherID, lowestID, sooner} {
		facts[f.dealID] = f
		scored = append(scored, briefScore(f, 50_000_00, briefTestClock))
	}
	for _, item := range scored[1:] {
		if item.Composite != scored[0].Composite {
			t.Fatalf("tie fixture broken: composites %v vs %v differ", item.Composite, scored[0].Composite)
		}
	}

	// Higher base value first; equal values → the sooner close; equal
	// values and dates → the lowest deal id; the smallest base last.
	queue, _ := briefQueue(scored, facts)
	want := []ids.UUID{richer.dealID, sooner.dealID, lowestID.dealID, higherID.dealID, poorer.dealID}
	for i, id := range want {
		if queue[i].DealID != id {
			t.Fatalf("tie-break order = %v, want [richer, sooner, lowest-id, higher-id, poorer]", queueDeals(queue))
		}
	}
}

// The queue is honestly short: fewer candidates than the target yield a
// genuinely shorter queue, and more are cut at the target — padding with
// below-bar deals is a failure, not a fallback.
func TestBriefQueueIsHonestlyShortNeverPadded(t *testing.T) {
	facts := map[ids.UUID]briefDealFacts{}
	var scored []BriefQueueItem
	for i := byte(0); i < 12; i++ {
		f := briefDealFacts{dealID: uuidAt(10 + i)}
		if i < 3 {
			f.winProbability = 90 // well above the bar
		} else {
			f.winProbability = 10 // 0.03 win + 0.04 timing + 0.06 momentum floor = 0.13, below the bar
			f.expectedClose = closeIn(200)
		}
		facts[f.dealID] = f
		scored = append(scored, briefScore(f, briefRevenueNormFallbackMinor, briefTestClock))
	}

	queue, candidates := briefQueue(scored, facts)
	if candidates != 3 || len(queue) != 3 {
		t.Fatalf("3 deals clear the bar but queue/candidates = %d/%d — the queue must not be padded", len(queue), candidates)
	}
	for _, item := range queue {
		if item.Composite < briefCandidateMinScore {
			t.Fatalf("queued item %s scores %.3f below the bar", item.DealID, item.Composite)
		}
	}

	// With more candidates than the target, the queue caps at the target.
	for i := range scored {
		f := facts[scored[i].DealID]
		f.winProbability = 90
		facts[f.dealID] = f
		scored[i] = briefScore(f, briefRevenueNormFallbackMinor, briefTestClock)
	}
	queue, candidates = briefQueue(scored, facts)
	if candidates != 12 || len(queue) != briefQueueTarget {
		t.Fatalf("12 candidates → queue %d (candidates %d), want the %d target", len(queue), candidates, briefQueueTarget)
	}
}

// The B-E05.12 gate: an unevidenced, below-bar, mis-ordered, or overlong
// queue is refused rather than shipped.
func TestBriefEvidenceGateRefusesDishonestQueues(t *testing.T) {
	sound := []BriefQueueItem{
		{DealID: uuidAt(1), Composite: 0.8, EvidenceIDs: []ids.UUID{uuidAt(1)}},
		{DealID: uuidAt(2), Composite: 0.4, EvidenceIDs: []ids.UUID{uuidAt(2)}},
	}
	if err := validateBriefEvidence(sound); err != nil {
		t.Fatalf("a sound queue must pass: %v", err)
	}

	noEvidence := []BriefQueueItem{{DealID: uuidAt(1), Composite: 0.8}}
	if err := validateBriefEvidence(noEvidence); err == nil {
		t.Error("an item with no evidence ids must be refused (evidence-or-omit)")
	}

	padded := []BriefQueueItem{{DealID: uuidAt(1), Composite: 0.10, EvidenceIDs: []ids.UUID{uuidAt(1)}}}
	if err := validateBriefEvidence(padded); err == nil {
		t.Error("a below-bar item must be refused (the queue is never padded)")
	}

	misordered := []BriefQueueItem{
		{DealID: uuidAt(1), Composite: 0.4, EvidenceIDs: []ids.UUID{uuidAt(1)}},
		{DealID: uuidAt(2), Composite: 0.8, EvidenceIDs: []ids.UUID{uuidAt(2)}},
	}
	if err := validateBriefEvidence(misordered); err == nil {
		t.Error("a queue out of composite order must be refused")
	}

	overlong := make([]BriefQueueItem, briefQueueTarget+1)
	for i := range overlong {
		overlong[i] = BriefQueueItem{DealID: uuidAt(byte(20 + i)), Composite: 0.8, EvidenceIDs: []ids.UUID{uuidAt(byte(20 + i))}}
	}
	if err := validateBriefEvidence(overlong); err == nil {
		t.Errorf("a queue past the %d target must be refused", briefQueueTarget)
	}
}

func queueDeals(queue []BriefQueueItem) []ids.UUID {
	out := make([]ids.UUID, len(queue))
	for i, item := range queue {
		out[i] = item.DealID
	}
	return out
}
