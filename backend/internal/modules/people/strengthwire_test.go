// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The §4 result's translation onto the contract shape. Three routes answer
// it — the person strength read, the organization roll-up, and the company
// view's contact list — so a mislabeled bucket or a dropped factor is wrong
// in three places at once.

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestStrengthBucketToWireCoversTheDomainVocabulary(t *testing.T) {
	cases := map[string]crmcontracts.RelationshipStrengthBucket{
		"weak":     crmcontracts.RelationshipStrengthBucketWeak,
		"moderate": crmcontracts.RelationshipStrengthBucketWarm,
		"strong":   crmcontracts.RelationshipStrengthBucketStrong,
		"none":     crmcontracts.RelationshipStrengthBucketDormant,
		// A value the domain never emits must still land on a declared
		// enum member — a wire value the contract does not declare is worse
		// than a conservative one.
		"something-new": crmcontracts.RelationshipStrengthBucketDormant,
	}
	for domain, want := range cases {
		if got := StrengthBucketToWire(domain); got != want {
			t.Errorf("StrengthBucketToWire(%q) = %q, want %q", domain, got, want)
		}
	}
}

func TestStrengthToWireDerivesDirectionFromTheTwoCounts(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	last := now.Add(-24 * time.Hour)
	wire := StrengthToWire(RelationshipStrength{
		Strength: 71, Bucket: "strong",
		Recency: 0.5, Frequency: 0.4, Reciprocity: 0.9,
		LastInteraction: &last, Inbound90d: 3, Outbound90d: 1,
	}, now)

	if wire.Score != 71 {
		t.Errorf("score = %d, want 71", wire.Score)
	}
	if wire.Bucket != crmcontracts.RelationshipStrengthBucketStrong {
		t.Errorf("bucket = %q, want strong", wire.Bucket)
	}
	// direction = 1 - |3-1|/(3+1) = 0.5
	if wire.Factors.Direction != 0.5 {
		t.Errorf("factors.direction = %v, want 0.5", wire.Factors.Direction)
	}
	if wire.Factors.Recency != 0.5 || wire.Factors.Frequency != 0.4 || wire.Factors.Reciprocity != 0.9 {
		t.Errorf("factors = %+v, want the three §4 terms carried verbatim", wire.Factors)
	}
	if wire.ComputedAt == nil || !wire.ComputedAt.Equal(now) {
		t.Errorf("computed_at = %v, want the read's instant %v", wire.ComputedAt, now)
	}
	if wire.LastInteraction == nil || !wire.LastInteraction.Equal(last) {
		t.Errorf("last_interaction = %v, want %v", wire.LastInteraction, last)
	}
}

// A relationship with no interaction at all has no direction to report —
// zero, never a division by zero.
func TestStrengthToWireReportsNoDirectionWithoutInteractions(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	wire := StrengthToWire(RelationshipStrength{Bucket: "none"}, now)
	if wire.Factors.Direction != 0 {
		t.Errorf("factors.direction = %v with no interactions, want 0", wire.Factors.Direction)
	}
	if wire.Bucket != crmcontracts.RelationshipStrengthBucketDormant {
		t.Errorf("bucket = %q, want dormant", wire.Bucket)
	}
}
