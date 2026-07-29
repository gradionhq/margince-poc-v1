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
