// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Manual creates meeting the PO-F chokepoint (dedupe.go) under the
// manual policy, which is stated per LANE because the exact tier does
// not give one answer (telegram-oa design §7.3 asks every caller for
// exactly this):
//
//   - A claimed EMAIL refuses. ensurePersonEmailsUnclaimed answers that
//     tier with the 409 contract before the chokepoint even runs, and
//     the unique index is the structural backstop under a race — so by
//     the time the ladder reads, the email lane has nothing left to say.
//   - A shared PHONE does not refuse. A household line or a switchboard
//     belongs to several real, different people, and the create contract
//     promises a 409 on an address alone (data-model §3.2). It creates
//     AND records: an exact hit routes PAST the fuzzy tier (routeExact
//     returns before scoring), so without a recording arm of its own a
//     create sharing a number with an existing record would leave LESS
//     trail than one that merely looked similar.
//   - A FUZZY near-match creates AND records — a probability never
//     blocks a human, but the pair must not vanish either.
//
// A manual create never sees routeExact's lane CONFLICT: two exact lanes
// must hit for that, a create carries no channel identity, and a claimed
// address has already been refused — so the phone is the only lane left
// to speak, and one voice cannot disagree.
//
// A recording is the DH-DDL-1 review queue itself (an open
// dedupe_candidate row the human dispositions); the fuzzy arm adds the
// append-only system_log ledger line for the score it acted on. Both sit
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
// BEFORE the insert — afterwards the new row would match itself. Both
// exact-key kinds the request carries are offered, and every value of
// each counts rather than just the primary: the addresses, whose lane is
// already silent because the claimed case was refused above, and the
// numbers, whose lane is the one exact signal this path can act on.
func manualDedupePerson(ctx context.Context, tx pgx.Tx, in CreatePersonInput) (PersonResolution, error) {
	emails := make([]string, 0, len(in.Emails))
	for _, e := range in.Emails {
		emails = append(emails, e.Email)
	}
	phones := make([]string, 0, len(in.Phones))
	for _, p := range in.Phones {
		phones = append(phones, p.Phone)
	}
	return DedupePerson(ctx, tx, PersonCandidate{FullName: in.FullName, Emails: emails, Phones: phones})
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

// recordIfReview leaves the review trail a manual person create owes —
// the fuzzy pair, and the shared phone the exact tier routed on. A
// no-match writes nothing, because there is no second record to compare.
func (m PersonResolution) recordIfReview(ctx context.Context, tx pgx.Tx, createdID ids.PersonID, createdName, source, by string) error {
	switch m.Decision {
	case DecisionFuzzyReview:
		var incumbent string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM person WHERE id = $1`, m.PersonID).Scan(&incumbent); err != nil {
			return fmt.Errorf("reading person near-match incumbent: %w", err)
		}
		return recordNearMatch(ctx, tx, entityPerson, createdID.UUID, m.PersonID.UUID, m.Confidence,
			nearMatchEvidence(fieldFullName, createdName, incumbent, m.Confidence), source, by)
	case DecisionExactCollision:
		return m.recordSharedPhone(ctx, tx, createdID, source, by)
	default:
		return nil
	}
}

// recordSharedPhone puts the pair behind an exact phone collision on the
// review queue: this create and the record that already holds the number.
// It is a proposal, never a merge — no key moves between the two records,
// and the disposition stays the human's.
//
// The number is read back from the two records rather than picked out of
// the request, so the evidence names the value the lane actually matched
// on in its stored E.164 form. That read cannot come up empty: the lane
// matched on numbers this create has just inserted, so an empty answer
// would mean the ladder and the child-row write disagree about what was
// stored, and a review pair with no evidence is worse than a loud failure.
func (m PersonResolution) recordSharedPhone(ctx context.Context, tx pgx.Tx, createdID ids.PersonID, source, by string) error {
	if m.MatchedLane != lanePhone {
		// The package comment above is the argument that this cannot
		// happen; if a lane ever does reach here it has no create-path
		// policy, and inventing one silently is how a 409 contract or a
		// merge rule gets bypassed unnoticed.
		return fmt.Errorf("people: manual person create routed on the %q exact lane, which has no create-path policy", m.MatchedLane)
	}
	var shared string
	if err := tx.QueryRow(ctx, `
		SELECT created.phone
		  FROM person_phone created
		  JOIN person_phone incumbent ON incumbent.phone = created.phone
		 WHERE created.person_id = $1 AND incumbent.person_id = $2
		   AND created.archived_at IS NULL AND incumbent.archived_at IS NULL
		 ORDER BY created.phone
		 LIMIT 1`, createdID, m.PersonID).Scan(&shared); err != nil {
		return fmt.Errorf("reading the phone number both records hold: %w", err)
	}
	// "collide" at its limit: the fuzzy tier uses it for two values that
	// resemble each other, and an exact key hit is the same statement with
	// the two sides equal. The confidence is the exact-key ceiling
	// identityconflict.go argues for — an established key on both records
	// outranks any similarity score, and sorts ahead of one in the queue.
	evidence := []map[string]any{{
		evidenceFieldKey:  fieldPhone,
		evidenceLeftKey:   shared,
		evidenceRightKey:  shared,
		evidenceSignalKey: evidenceSignalCollide,
		evidenceScoreKey:  identityConflictConfidence,
	}}
	if _, err := recordDedupeCandidate(ctx, tx, entityPerson, createdID.UUID, m.PersonID.UUID,
		identityConflictConfidence, evidence, source, by); err != nil {
		return fmt.Errorf("record person shared-phone candidate: %w", err)
	}
	return nil
}

func (m OrganizationMatch) recordIfReview(ctx context.Context, tx pgx.Tx, createdID ids.OrganizationID, createdName, source, by string) error {
	if m.Decision != DecisionFuzzyReview {
		return nil
	}
	var incumbent string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM organization WHERE id = $1`, m.OrganizationID).Scan(&incumbent); err != nil {
		return fmt.Errorf("reading organization near-match incumbent: %w", err)
	}
	return recordNearMatch(ctx, tx, entityOrganization, createdID.UUID, m.OrganizationID.UUID, m.Confidence,
		nearMatchEvidence(fieldDisplayName, createdName, incumbent, m.Confidence), source, by)
}

// nearMatchEvidence is the detection-time snapshot the review queue
// renders (DH-N-8) — the same shape ensure.go captures for connector
// creates: the colliding name pair and the PO-F score behind it.
func nearMatchEvidence(field, created, incumbent string, confidence float64) []map[string]any {
	return []map[string]any{
		{evidenceFieldKey: field, evidenceLeftKey: created, evidenceRightKey: incumbent, evidenceSignalKey: evidenceSignalCollide, evidenceScoreKey: confidence},
	}
}

// recordNearMatch leaves the fuzzy pair for review: one open
// dedupe_candidate row (DH-DDL-1 — the queue the human actually works)
// plus the append-only dedupe_near_match ledger line, both inside the
// create's own transaction so the record and its review trail commit or
// roll back together.
func recordNearMatch(ctx context.Context, tx pgx.Tx, entityType string, createdID, matchedID ids.UUID, confidence float64, evidence []map[string]any, source, by string) error {
	if _, err := recordDedupeCandidate(ctx, tx, entityType, createdID, matchedID, confidence, evidence, source, by); err != nil {
		return fmt.Errorf("record %s near-match candidate: %w", entityType, err)
	}
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
