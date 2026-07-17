// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The dedupe parameters (PO-F-1/PO-F-2). Source constants, not runtime
// config: the spec's registry pins them outside the runtime-config
// boundary because a workspace tuning its own match threshold would make
// "no duplicates" unauditable across installations.
const (
	dedupeReviewThreshold = 0.72
	dedupeNameWeight      = 0.55
	dedupeOrgDomainWeight = 0.45
)

// DedupeDecision is the closed outcome set of PO-F-1/PO-F-2. Fuzzy never
// resolves itself: DEDUPE_FUZZY_AUTOMERGE is pinned *(never)*, so the
// only automatic resolutions are exact-key ones.
type DedupeDecision string

const (
	// DecisionExactCollision is a unique-key hit: same email, or same
	// org domain. Deterministic, no score. The caller's policy decides
	// whether that blocks (API) or lands on the incumbent (capture).
	DecisionExactCollision DedupeDecision = "exact_collision"
	// DecisionFuzzyReview is a near-match at or above the threshold: a
	// human compares the two records side by side. Never a merge.
	DecisionFuzzyReview DedupeDecision = "fuzzy_review"
	// DecisionNoMatch means create.
	DecisionNoMatch DedupeDecision = "no_match"
)

// PersonCandidate is the input to PO-F-1 — the fields the formula reads,
// not a whole person: a resolver that took CreatePersonInput could not
// serve capture, promote, and the public booking surface alike.
type PersonCandidate struct {
	FullName string
	// Emails are checked in full against the exact tier; every email on
	// the candidate counts, not just the primary.
	Emails []string
	// CurrentPrimaryOrgID drives org_match = 1.0 when both sides share
	// an employer. Nil when the candidate has no known employer yet.
	CurrentPrimaryOrgID *ids.OrganizationID
}

// PersonMatch is PO-F-1's output: the decision plus the person it names.
type PersonMatch struct {
	Decision   DedupeDecision
	PersonID   ids.PersonID
	Confidence float64
}

// DedupePerson is PO-F-1, the single person-matching implementation —
// "one dedupe implementation, not two". It reads; it never writes and
// never merges. Callers map the decision onto their own policy.
func DedupePerson(ctx context.Context, tx pgx.Tx, c PersonCandidate) (PersonMatch, error) {
	if hit, found, err := exactPersonByEmail(ctx, tx, c.Emails); err != nil || found {
		return PersonMatch{Decision: DecisionExactCollision, PersonID: hit}, err
	}
	// A nameless captured contact never fuzzy-matches: with no name there
	// is nothing to score, and org_match alone would collide every
	// colleague onto one record.
	if normalizeName(c.FullName) == "" {
		return PersonMatch{Decision: DecisionNoMatch}, nil
	}
	return fuzzyPerson(ctx, tx, c)
}

// exactPersonByEmail is PO-F-1 tier 1. Every candidate email is checked;
// the lowest person id wins so a candidate colliding on two emails
// against two people resolves the same way on every run.
func exactPersonByEmail(ctx context.Context, tx pgx.Tx, emails []string) (ids.PersonID, bool, error) {
	if len(emails) == 0 {
		return ids.PersonID{}, false, nil
	}
	lowered := make([]string, 0, len(emails))
	for _, e := range emails {
		lowered = append(lowered, normalizeEmail(e))
	}
	var id ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT person_id FROM person_email
		WHERE email = ANY($1) AND archived_at IS NULL
		ORDER BY person_id
		LIMIT 1`, lowered).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.PersonID{}, false, nil
	}
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("dedupe person exact tier: %w", err)
	}
	return id, true, nil
}

// personCandidateRow is one row of the restricted candidate set.
type personCandidateRow struct {
	id        ids.PersonID
	fullName  string
	orgID     *ids.OrganizationID
	orgDomain *string
}

// fuzzyPerson is PO-F-1 tier 2. The candidate set is restricted to
// people sharing a name trigram or the candidate's employer — the
// formula's own bound, so scoring stays inside the create budget instead
// of walking the workspace.
func fuzzyPerson(ctx context.Context, tx pgx.Tx, c PersonCandidate) (PersonMatch, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.full_name, r.organization_id, od.domain
		  FROM person p
		  LEFT JOIN relationship r
		    ON r.person_id = p.id AND r.kind = 'employment'
		   AND r.is_current_primary AND r.archived_at IS NULL
		  LEFT JOIN organization_domain od
		    ON od.organization_id = r.organization_id AND od.archived_at IS NULL
		 WHERE p.archived_at IS NULL
		   AND (f_fold_apostrophes(lower(p.full_name)) % f_fold_apostrophes(lower($1))
		        OR ($2::uuid IS NOT NULL AND r.organization_id = $2))`,
		c.FullName, c.CurrentPrimaryOrgID)
	if err != nil {
		return PersonMatch{}, fmt.Errorf("dedupe person candidate set: %w", err)
	}
	defer rows.Close()

	best := PersonMatch{Decision: DecisionNoMatch}
	for rows.Next() {
		var row personCandidateRow
		if err := rows.Scan(&row.id, &row.fullName, &row.orgID, &row.orgDomain); err != nil {
			return PersonMatch{}, fmt.Errorf("scan person candidate: %w", err)
		}
		confidence := personConfidence(c, row)
		// Equal confidence resolves to the lowest person id — a total
		// order, so the queue does not shuffle between runs.
		if confidence > best.Confidence ||
			(confidence == best.Confidence && best.PersonID != (ids.PersonID{}) && row.id.String() < best.PersonID.String()) {
			best.Confidence, best.PersonID = confidence, row.id
		}
	}
	if err := rows.Err(); err != nil {
		return PersonMatch{}, fmt.Errorf("drain person candidates: %w", err)
	}
	if best.Confidence >= dedupeReviewThreshold {
		best.Decision = DecisionFuzzyReview
		return best, nil
	}
	return PersonMatch{Decision: DecisionNoMatch}, nil
}

// personConfidence is the PO-F-1 score: weights sum to 1.0, so the
// result is in [0,1] and comparable against the threshold directly.
func personConfidence(c PersonCandidate, row personCandidateRow) float64 {
	return dedupeNameWeight*nameSimilarity(c.FullName, row.fullName) +
		dedupeOrgDomainWeight*orgMatch(c, row)
}

// orgMatch is PO-F-1's employer agreement term, most-specific first: a
// shared employer row beats a shared email domain.
func orgMatch(c PersonCandidate, row personCandidateRow) float64 {
	if c.CurrentPrimaryOrgID != nil && row.orgID != nil && *c.CurrentPrimaryOrgID == *row.orgID {
		return 1.0
	}
	if row.orgDomain != nil && candidateSharesDomain(c, *row.orgDomain) {
		return 0.8
	}
	return 0.0
}

// candidateSharesDomain reports whether any candidate email sits on an
// organization domain the incumbent is mapped to.
func candidateSharesDomain(c PersonCandidate, domain string) bool {
	for _, e := range c.Emails {
		if emailDomain(e) == normalizeDomain(domain) {
			return true
		}
	}
	return false
}

// normalizeEmail matches how person_email stores the address: the insert
// path lowercases on write, so the exact tier compares like for like.
func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// normalizeDomain matches organization_domain's storage contract:
// lowercase only — never unaccent, or münich.example would collide with
// a different organization's munich.example.
func normalizeDomain(d string) string { return strings.ToLower(strings.TrimSpace(d)) }

// emailDomain returns the lowercased host of an address, or "" when the
// input carries no host to compare.
func emailDomain(e string) string {
	at := strings.LastIndex(normalizeEmail(e), "@")
	if at < 0 {
		return ""
	}
	return normalizeEmail(e)[at+1:]
}

// OrganizationCandidate is the input to PO-F-2.
type OrganizationCandidate struct {
	DisplayName string
	// Domains are the candidate's claimed domains; a free-mail domain
	// must be filtered by the caller before it reaches here — this
	// resolver matches domains, it does not judge them.
	Domains []string
}

// OrganizationMatch is PO-F-2's output.
type OrganizationMatch struct {
	Decision       DedupeDecision
	OrganizationID ids.OrganizationID
	Confidence     float64
}

// DedupeOrganization is PO-F-2 — the org half of the one dedupe
// implementation. Domain is the exact key; name similarity alone is the
// fuzzy tier, because without a domain there is nothing to anchor on.
func DedupeOrganization(ctx context.Context, tx pgx.Tx, c OrganizationCandidate) (OrganizationMatch, error) {
	if hit, found, err := exactOrgByDomain(ctx, tx, c.Domains); err != nil || found {
		return OrganizationMatch{Decision: DecisionExactCollision, OrganizationID: hit}, err
	}
	if normalizeOrgName(c.DisplayName) == "" {
		return OrganizationMatch{Decision: DecisionNoMatch}, nil
	}
	return fuzzyOrganization(ctx, tx, c)
}

// exactOrgByDomain is PO-F-2 tier 1: any candidate domain already mapped
// to a live org. This is also the capture employer-inference path — a
// domain hit lands the person on the existing company.
func exactOrgByDomain(ctx context.Context, tx pgx.Tx, domains []string) (ids.OrganizationID, bool, error) {
	if len(domains) == 0 {
		return ids.OrganizationID{}, false, nil
	}
	lowered := make([]string, 0, len(domains))
	for _, d := range domains {
		lowered = append(lowered, normalizeDomain(d))
	}
	var id ids.OrganizationID
	err := tx.QueryRow(ctx, `
		SELECT organization_id FROM organization_domain
		WHERE domain = ANY($1) AND archived_at IS NULL
		ORDER BY organization_id
		LIMIT 1`, lowered).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.OrganizationID{}, false, nil
	}
	if err != nil {
		return ids.OrganizationID{}, false, fmt.Errorf("dedupe org exact tier: %w", err)
	}
	return id, true, nil
}

// fuzzyOrganization scores name similarity over the trigram-restricted
// candidate set. "Acme Inc" and "Acme GmbH" both normalize to "acme" and
// land here — different legal entities are a human's call, not a merge.
// The trigram filter is recall-only narrowing (scoring below is the
// authority), so the candidate side is suffix-stripped to match what the
// score compares; the stored side keeps its suffix, whose few trigrams
// barely dent the similarity of a shared stem.
func fuzzyOrganization(ctx context.Context, tx pgx.Tx, c OrganizationCandidate) (OrganizationMatch, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, display_name FROM organization
		 WHERE archived_at IS NULL
		   AND f_fold_apostrophes(lower(display_name)) % f_fold_apostrophes(lower($1))`,
		normalizeOrgName(c.DisplayName))
	if err != nil {
		return OrganizationMatch{}, fmt.Errorf("dedupe org candidate set: %w", err)
	}
	defer rows.Close()

	best := OrganizationMatch{Decision: DecisionNoMatch}
	for rows.Next() {
		var id ids.OrganizationID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return OrganizationMatch{}, fmt.Errorf("scan org candidate: %w", err)
		}
		confidence := nameSimilarity(normalizeOrgName(c.DisplayName), normalizeOrgName(name))
		if confidence > best.Confidence ||
			(confidence == best.Confidence && best.OrganizationID != (ids.OrganizationID{}) && id.String() < best.OrganizationID.String()) {
			best.Confidence, best.OrganizationID = confidence, id
		}
	}
	if err := rows.Err(); err != nil {
		return OrganizationMatch{}, fmt.Errorf("drain org candidates: %w", err)
	}
	if best.Confidence >= dedupeReviewThreshold {
		best.Decision = DecisionFuzzyReview
		return best, nil
	}
	return OrganizationMatch{Decision: DecisionNoMatch}, nil
}
