// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relstrength

// The fold against the spec's own numbers. formulas-and-rules §4 carries a
// worked example with every intermediate value written out; if this package
// and that paragraph ever disagree, one of them is wrong and a test that only
// checked "roughly warm" would not say which.

import (
	"math"
	"testing"
	"time"
)

func at(now time.Time, daysAgo float64) *time.Time {
	t := now.Add(-time.Duration(daysAgo * float64(24*time.Hour)))
	return &t
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 0.001 }

func TestTheWorkedExampleFromTheSpec(t *testing.T) {
	// §4, verbatim: last interaction 5 days ago, 12 interactions in 90 days,
	// 7 inbound and 5 outbound → 47, moderate.
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	got := Compute(Inputs{
		LastInteraction: at(now, 5), Count90d: 12, Inbound90d: 7, Outbound90d: 5,
	}, now)

	if !closeTo(got.Recency, 0.891) {
		t.Errorf("recency = %.4f, want 0.891 (2^(-5/30))", got.Recency)
	}
	if !closeTo(got.Frequency, 0.60) {
		t.Errorf("frequency = %.4f, want 0.60 (12/20)", got.Frequency)
	}
	if !closeTo(got.Reciprocity, 0.875) {
		t.Errorf("reciprocity = %.4f, want 0.875 (0.25 + 0.75 * 0.833)", got.Reciprocity)
	}
	if got.Strength != 47 {
		t.Errorf("strength = %d, want 47 — the spec's worked example", got.Strength)
	}
	if got.Bucket != BucketModerate {
		t.Errorf("bucket = %q, want %q", got.Bucket, BucketModerate)
	}
}

func TestNeverHavingSpokenIsNotAScoreOfZero(t *testing.T) {
	// "We have never spoken" and "we spoke and it went cold" are different
	// facts about an account, and only one of them is a number. Rendering the
	// first as 0 tells a rep a relationship decayed when none ever existed.
	got := Compute(Inputs{}, time.Now())
	if got.Bucket != BucketNone {
		t.Errorf("no interactions → bucket %q, want %q", got.Bucket, BucketNone)
	}
	if got.Strength != 0 || got.Recency != 0 || got.Frequency != 0 {
		t.Errorf("no interactions produced factors %+v, want the zero score", got)
	}
}

func TestOneWayTrafficFloorsReciprocity(t *testing.T) {
	// A hundred unanswered sends is not a relationship. The floor is what
	// admits they happened without letting them score as one.
	now := time.Now()
	oneWay := Compute(Inputs{LastInteraction: at(now, 1), Count90d: 100, Outbound90d: 100}, now)
	if oneWay.Reciprocity != ReciprocityFloor {
		t.Errorf("purely outbound reciprocity = %v, want the %v floor", oneWay.Reciprocity, ReciprocityFloor)
	}
	// Perfectly balanced traffic, same volume and recency, must beat it.
	twoWay := Compute(Inputs{LastInteraction: at(now, 1), Count90d: 100, Inbound90d: 50, Outbound90d: 50}, now)
	if twoWay.Strength <= oneWay.Strength {
		t.Errorf("a two-way exchange scored %d, no better than a one-way blast at %d",
			twoWay.Strength, oneWay.Strength)
	}
}

func TestUndirectedInteractionsCountForFrequencyButNotReciprocity(t *testing.T) {
	// Inbound + outbound need not sum to Count90d: an interaction with no
	// recorded direction is evidence that contact happened and says nothing
	// about who reached out. It must not be silently read as one-way.
	now := time.Now()
	got := Compute(Inputs{LastInteraction: at(now, 1), Count90d: 20}, now)
	if !closeTo(got.Frequency, 1.0) {
		t.Errorf("frequency = %.4f, want 1.0 — undirected interactions still count", got.Frequency)
	}
	if got.Reciprocity != ReciprocityFloor {
		t.Errorf("reciprocity = %v with no directed traffic, want the floor rather than a divide by zero", got.Reciprocity)
	}
}

func TestAFutureTimestampDoesNotAmplifyRecency(t *testing.T) {
	// Clock skew between the app and the database is ordinary. Left
	// unclamped, 2^(-negative/30) exceeds 1 and inflates every factor above
	// it — a score over 100 on a record nobody can explain.
	now := time.Now()
	got := Compute(Inputs{LastInteraction: at(now, -3), Count90d: 20, Inbound90d: 10, Outbound90d: 10}, now)
	if got.Recency > 1.0 {
		t.Errorf("recency = %v for a future interaction, want it clamped to 1.0", got.Recency)
	}
	if got.Strength > 100 {
		t.Errorf("strength = %d, want at most 100", got.Strength)
	}
}

func TestTheFoldIsPure(t *testing.T) {
	// The score is computed on every read rather than stored, which is only
	// safe if the same inputs and the same clock always give the same answer.
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	in := Inputs{LastInteraction: at(now, 7), Count90d: 9, Inbound90d: 4, Outbound90d: 5}
	first := Compute(in, now)
	for i := 0; i < 5; i++ {
		if got := Compute(in, now); got != first {
			t.Fatalf("run %d gave %+v, want %+v — the fold reads something outside its inputs", i, got, first)
		}
	}
}

func TestBucketBoundaries(t *testing.T) {
	// The bands are quoted to users ("strong", "moderate"), so the boundary
	// the code compares against must be the boundary support explains.
	for _, c := range []struct {
		strength int
		want     string
	}{
		{0, BucketWeak},
		{24, BucketWeak},
		{25, BucketModerate},
		{59, BucketModerate},
		{60, BucketStrong},
		{100, BucketStrong},
	} {
		if got := bucketFor(c.strength); got != c.want {
			t.Errorf("bucketFor(%d) = %q, want %q", c.strength, got, c.want)
		}
	}
}
