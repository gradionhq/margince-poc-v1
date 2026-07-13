// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func TestStrengthBucketMapsGoToContractVocabulary(t *testing.T) {
	cases := map[string]crmcontracts.RelationshipStrengthBucket{
		"none":     crmcontracts.RelationshipStrengthBucketDormant,
		"weak":     crmcontracts.RelationshipStrengthBucketWeak,
		"moderate": crmcontracts.RelationshipStrengthBucketWarm,
		"strong":   crmcontracts.RelationshipStrengthBucketStrong,
	}
	for in, want := range cases {
		if got := strengthBucketToWire(in); got != want {
			t.Fatalf("strengthBucketToWire(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStrengthToWireCarriesScoreFactorsAndDirection(t *testing.T) {
	// inbound=5 outbound=7 → balance = 1 - |5-7|/12 = 0.8333…
	rs := people.RelationshipStrength{
		Strength: 72, Bucket: "strong",
		Recency: 0.9, Frequency: 0.6, Reciprocity: 0.8,
		InteractionCount90d: 12, Inbound90d: 5, Outbound90d: 7,
	}
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	wire := strengthToWire(rs, now)
	if wire.Score != 72 || wire.Bucket != crmcontracts.RelationshipStrengthBucketStrong {
		t.Fatalf("score/bucket wrong: %+v", wire)
	}
	if wire.Factors.Recency != 0.9 || wire.Factors.Reciprocity != 0.8 {
		t.Fatalf("factors wrong: %+v", wire.Factors)
	}
	if d := wire.Factors.Direction; d < 0.83 || d > 0.834 {
		t.Fatalf("direction = %v, want ~0.8333", d)
	}
	if wire.Inbound90d == nil || *wire.Inbound90d != 5 || wire.ComputedAt == nil {
		t.Fatalf("counts/computed_at wrong: %+v", wire)
	}
}
