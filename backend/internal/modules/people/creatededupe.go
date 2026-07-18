// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Manual creates meeting the PO-F chokepoint (dedupe.go) under the
// manual policy: exact→refuse (the unclaimed pre-checks and unique
// indexes already answer that tier with the 409 contract), fuzzy→create
// AND record — a probability never blocks a human, but the pair must not
// vanish either. Until the dedupe_candidate queue lands (DH-DDL-1), the
// system_log ledger is the recording mechanism: one append-only line
// inside the create's own transaction, so the record and its review
// trail commit or roll back together.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// manualDedupePerson runs PO-F-1 for a manual person create. It must run
// BEFORE the insert — afterwards the new row would match itself. The
// exact tier cannot fire here: ensurePersonEmailsUnclaimed already
// refused every claimed address in this same transaction, so the
// chokepoint's remaining signal is the fuzzy tier. Every address on the
// candidate counts, not just the primary.
func manualDedupePerson(ctx context.Context, tx pgx.Tx, in CreatePersonInput) (PersonMatch, error) {
	emails := make([]string, 0, len(in.Emails))
	for _, e := range in.Emails {
		emails = append(emails, e.Email)
	}
	return DedupePerson(ctx, tx, PersonCandidate{FullName: in.FullName, Emails: emails})
}

// manualDedupeOrganization runs PO-F-2 for a manual organization create,
// before the insert for the same self-match reason. The domains are the
// org's own claimed domains, not derived email hosts, so the free-mail
// filtering PO-F-2 delegates to callers does not apply here — a manual
// claim of gmail.com should still collide. The exact tier cannot fire:
// ensureOrgDomainsUnclaimed already refused every claimed domain.
func manualDedupeOrganization(ctx context.Context, tx pgx.Tx, in CreateOrganizationInput) (OrganizationMatch, error) {
	domains := make([]string, 0, len(in.Domains))
	for _, d := range in.Domains {
		domains = append(domains, d.Domain)
	}
	return DedupeOrganization(ctx, tx, OrganizationCandidate{DisplayName: in.DisplayName, Domains: domains})
}

// recordIfReview leaves the review trail when the match is a fuzzy hit;
// any other decision writes nothing.
func (m PersonMatch) recordIfReview(ctx context.Context, tx pgx.Tx, createdID ids.PersonID) error {
	if m.Decision != DecisionFuzzyReview {
		return nil
	}
	return recordNearMatch(ctx, tx, "person", createdID.UUID, m.PersonID.UUID, m.Confidence)
}

func (m OrganizationMatch) recordIfReview(ctx context.Context, tx pgx.Tx, createdID ids.OrganizationID) error {
	if m.Decision != DecisionFuzzyReview {
		return nil
	}
	return recordNearMatch(ctx, tx, "organization", createdID.UUID, m.OrganizationID.UUID, m.Confidence)
}

// recordNearMatch writes the one append-only dedupe_near_match ledger
// line naming the created record, the incumbent it nearly matched, and
// the PO-F confidence that put the pair over the review threshold.
func recordNearMatch(ctx context.Context, tx pgx.Tx, entityType string, createdID, matchedID ids.UUID, confidence float64) error {
	if _, err := storekit.LogSystem(ctx, tx, "dedupe_near_match", map[string]any{
		"entity_type": entityType,
		"created_id":  createdID.String(),
		"matched_id":  matchedID.String(),
		"confidence":  confidence,
	}); err != nil {
		return fmt.Errorf("record %s near-match: %w", entityType, err)
	}
	return nil
}
