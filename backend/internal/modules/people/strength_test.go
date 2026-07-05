// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"math"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
}

func at(daysAgo float64) *time.Time {
	t := fixedNow().Add(-time.Duration(daysAgo * 24 * float64(time.Hour)))
	return &t
}

// The spec's worked example (formulas-and-rules §4.1): last touch 5 days
// ago, 12 interactions in 90d, 7 inbound / 5 outbound → 47, moderate.
func TestStrengthReproducesTheWorkedExample(t *testing.T) {
	r := RelationshipStrength{
		LastInteraction:     at(5),
		InteractionCount90d: 12,
		Inbound90d:          7,
		Outbound90d:         5,
	}
	r.finish(fixedNow())
	if r.Strength != 47 || r.Bucket != "moderate" {
		t.Fatalf("worked example → %d (%s), want 47 (moderate)", r.Strength, r.Bucket)
	}
}

// The score reconciles exactly to its three named factors — no opaque
// term (P6 factor-decomposition acceptance).
func TestStrengthDecomposesToItsFactors(t *testing.T) {
	r := RelationshipStrength{
		LastInteraction:     at(12),
		InteractionCount90d: 8,
		Inbound90d:          2,
		Outbound90d:         6,
	}
	r.finish(fixedNow())
	recomposed := int(math.Round(100 * r.Recency * r.Frequency * r.Reciprocity))
	if r.Strength != recomposed {
		t.Fatalf("score %d does not reconcile to its factors (%f × %f × %f)", r.Strength, r.Recency, r.Frequency, r.Reciprocity)
	}
}

// Fixed inputs + a fixed clock → a stable value; advancing the clock
// decays ONLY recency, on the 2^(−t/30d) primitive.
func TestStrengthDecaysWithTheClock(t *testing.T) {
	base := RelationshipStrength{LastInteraction: at(0), InteractionCount90d: 12, Inbound90d: 6, Outbound90d: 6}
	fresh := base
	fresh.finish(fixedNow())
	aged := base
	aged.finish(fixedNow().AddDate(0, 0, 30))
	if fresh.Strength <= aged.Strength {
		t.Fatalf("advancing the clock must decay strength: %d → %d", fresh.Strength, aged.Strength)
	}
	// One half-life halves recency exactly; the other factors hold.
	if math.Abs(aged.Recency-fresh.Recency/2) > 1e-9 {
		t.Fatalf("30 days must halve recency: %f → %f", fresh.Recency, aged.Recency)
	}
	if aged.Frequency != fresh.Frequency || aged.Reciprocity != fresh.Reciprocity {
		t.Fatal("the clock may only move recency")
	}
}

func TestStrengthEdgeCases(t *testing.T) {
	// No interactions: 0 and "none" — shown as a state, not a number.
	empty := RelationshipStrength{}
	empty.finish(fixedNow())
	if empty.Strength != 0 || empty.Bucket != "none" {
		t.Fatalf("no interactions → %d (%s), want 0 (none)", empty.Strength, empty.Bucket)
	}

	// Purely one-directional: reciprocity sits exactly on the floor.
	oneWay := RelationshipStrength{LastInteraction: at(1), InteractionCount90d: 10, Inbound90d: 0, Outbound90d: 10}
	oneWay.finish(fixedNow())
	if oneWay.Reciprocity != relStrengthReciprocityFloor {
		t.Fatalf("one-directional reciprocity = %f, want the %v floor", oneWay.Reciprocity, relStrengthReciprocityFloor)
	}

	// A single interaction today: strong recency, tiny frequency — one
	// email is not a relationship.
	single := RelationshipStrength{LastInteraction: at(0), InteractionCount90d: 1, Inbound90d: 1, Outbound90d: 0}
	single.finish(fixedNow())
	if single.Strength >= 25 {
		t.Fatalf("a single interaction scored %d — must stay weak", single.Strength)
	}
}
