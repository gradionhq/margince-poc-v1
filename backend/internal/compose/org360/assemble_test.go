// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The pure shape translations between the §4 domain result and the two
// contract schemas. They carry no database, so they are pinned here rather
// than in the integration lane: what can go wrong is a mislabeled bucket or
// a dropped factor, and both are arithmetic.

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBucketToWireCoversTheDomainVocabulary(t *testing.T) {
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
		if got := bucketToWire(domain); got != want {
			t.Errorf("bucketToWire(%q) = %q, want %q", domain, got, want)
		}
	}
}

func TestStrengthToWireDerivesDirectionFromTheTwoCounts(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	last := now.Add(-24 * time.Hour)
	wire := strengthToWire(people.RelationshipStrength{
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
	wire := strengthToWire(people.RelationshipStrength{Bucket: "none"}, now)
	if wire.Factors.Direction != 0 {
		t.Errorf("factors.direction = %v with no interactions, want 0", wire.Factors.Direction)
	}
	if wire.Bucket != crmcontracts.RelationshipStrengthBucketDormant {
		t.Errorf("bucket = %q, want dormant", wire.Bucket)
	}
}

func TestAccountStrengthToWireCarriesTheContributorAndCount(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	contributor := ids.From[ids.PersonKind](ids.NewV7())
	wire := accountStrengthToWire(people.AccountStrength{
		RelationshipStrength: people.RelationshipStrength{Strength: 62, Bucket: "strong", Inbound90d: 2, Outbound90d: 2},
		ContributorPersonID:  &contributor,
		ContactCount:         4,
	}, now)

	if wire.Score != 62 {
		t.Errorf("score = %d, want 62", wire.Score)
	}
	if wire.ContactCount != 4 {
		t.Errorf("contact_count = %d, want 4", wire.ContactCount)
	}
	if wire.ContributorPersonId == nil || ids.UUID(*wire.ContributorPersonId) != contributor.UUID {
		t.Errorf("contributor_person_id = %v, want %v", wire.ContributorPersonId, contributor)
	}
	if wire.Bucket != crmcontracts.OrganizationStrengthBucket(crmcontracts.RelationshipStrengthBucketStrong) {
		t.Errorf("bucket = %q, want strong", wire.Bucket)
	}
}

// An account with no contact the caller can read has a score of nobody:
// the contributor is null rather than a zero uuid pointing at no one.
func TestAccountStrengthToWireLeavesTheContributorNullWithoutContacts(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	wire := accountStrengthToWire(people.AccountStrength{
		RelationshipStrength: people.RelationshipStrength{Bucket: "none"},
	}, now)
	if wire.ContributorPersonId != nil {
		t.Errorf("contributor_person_id = %v for an account with no visible contact, want null", wire.ContributorPersonId)
	}
	if wire.ContactCount != 0 {
		t.Errorf("contact_count = %d, want 0", wire.ContactCount)
	}
}

func TestPageInfoOmitsACursorItDoesNotHave(t *testing.T) {
	if got := pageInfo(storekit.Page{}); got.HasMore || got.NextCursor != nil {
		t.Errorf("pageInfo(zero) = %+v, want has_more false and no cursor", got)
	}
	got := pageInfo(storekit.Page{HasMore: true, NextCursor: "abc"})
	if !got.HasMore || got.NextCursor == nil || *got.NextCursor != "abc" {
		t.Errorf("pageInfo = %+v, want has_more true carrying the cursor", got)
	}
}

func TestTruncateFlagsOnlyASectionItActuallyCut(t *testing.T) {
	exact := make([]int, sectionLimit)
	rows, page := truncate(exact)
	if len(rows) != sectionLimit || page.HasMore {
		t.Errorf("truncate(exactly the limit) = %d rows, has_more %v — a full page is not a cut one",
			len(rows), page.HasMore)
	}
	over := make([]int, sectionLimit+1)
	rows, page = truncate(over)
	if len(rows) != sectionLimit || !page.HasMore {
		t.Errorf("truncate(over the limit) = %d rows, has_more %v — the caller must learn it was cut",
			len(rows), page.HasMore)
	}
}
