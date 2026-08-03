// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// PO-F-2, the organization half of the one dedupe implementation. It lives
// beside the person half (dedupe.go) rather than inside it because the two
// ladders share only their vocabulary — the decision set, the threshold and
// the name-similarity function — while their tiers read different tables and
// weigh different evidence.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OrganizationCandidate is the input to PO-F-2.
type OrganizationCandidate struct {
	DisplayName string
	// LegalName is the registered entity behind the display name. PO-F-2
	// reads both because the same company is routinely captured twice under
	// two spellings — a marketing name from one domain, the registered form
	// from another — and the pair only collides on the legal axis.
	LegalName string
	// Domains are the candidate's claimed domains; a free-mail domain
	// must be filtered by the caller before it reaches here — this
	// resolver matches domains, it does not judge them.
	Domains []string
	// ExcludeID drops one organization from both tiers. The rename re-check
	// scores an EXISTING row against its neighbours: without this it matches
	// itself on every key it holds and masks every real rival behind a
	// perfect self-score.
	ExcludeID *ids.OrganizationID
}

// OrganizationMatch is PO-F-2's output.
type OrganizationMatch struct {
	Decision       DedupeDecision
	OrganizationID ids.OrganizationID
	Confidence     float64
	// Ranked carries every candidate at or above the threshold, best first.
	// The queue records ONE pair, but the best pair may already have been
	// dispositioned `not_a_duplicate`, and a single-winner result would let
	// that dismissal mask a genuine duplicate behind it forever.
	Ranked []OrganizationCandidateScore
}

// OrganizationCandidateScore is one scored rival from the fuzzy tier, with the
// two values that actually produced the score. The review queue renders those
// side by side, and a pair scored on the registered name must not be shown as
// a display-name collision — a reviewer deciding a merge is reading exactly
// that comparison.
type OrganizationCandidateScore struct {
	OrganizationID ids.OrganizationID
	Confidence     float64
	// MatchedField is the axis the winning pairing came from on the STORED
	// side: "display_name" or "legal_name".
	MatchedField string
	// CandidateValue and IncumbentValue are the two names compared.
	CandidateValue string
	IncumbentValue string
}

// DedupeOrganization is PO-F-2 — the org half of the one dedupe
// implementation. Domain is the exact key; name similarity alone is the
// fuzzy tier, because without a domain there is nothing to anchor on.
func DedupeOrganization(ctx context.Context, tx pgx.Tx, c OrganizationCandidate) (OrganizationMatch, error) {
	if hit, found, err := exactOrgByDomain(ctx, tx, c.Domains, c.ExcludeID); err != nil || found {
		return OrganizationMatch{Decision: DecisionExactCollision, OrganizationID: hit}, err
	}
	if NormalizeOrgName(c.DisplayName) == "" && NormalizeOrgName(c.LegalName) == "" {
		return OrganizationMatch{Decision: DecisionNoMatch}, nil
	}
	return fuzzyOrganization(ctx, tx, c)
}

// DedupeOrganizationForCreate is PO-F-2 for a path that is about to MINT a
// row, which needs one thing the read alone cannot give: serialization.
//
// The name axis has no unique index — two organizations may legitimately share
// a name — so nothing structural stops two creates converging on one. Without
// the lock, "Baqend" and "Baqend GmbH" landing concurrently each read before
// the other committed, each scored no_match, and both committed with NO pair on
// the queue: a duplicate nothing would ever re-detect, because the re-check
// only runs on a later rename. It is the same reason lockPhoneLane exists on
// the person side, and the lock is taken BEFORE the ladder reads so the loser
// sees the winner's committed row.
func DedupeOrganizationForCreate(ctx context.Context, tx pgx.Tx, c OrganizationCandidate) (OrganizationMatch, error) {
	if err := lockOrgNameWrites(ctx, tx); err != nil {
		return OrganizationMatch{}, err
	}
	return DedupeOrganization(ctx, tx, c)
}

// exactOrgByDomain is PO-F-2 tier 1: any candidate domain already mapped
// to a live org. This is also the capture employer-inference path — a
// domain hit lands the person on the existing company.
func exactOrgByDomain(ctx context.Context, tx pgx.Tx, domains []string, exclude *ids.OrganizationID) (ids.OrganizationID, bool, error) {
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
		  AND ($2::uuid IS NULL OR organization_id <> $2)
		ORDER BY organization_id
		LIMIT 1`, lowered, exclude).Scan(&id)
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
// Both name axes are compared on both sides: a record's registered name may
// be stored as its display name and vice versa, so the score is the best of
// the four pairings rather than display-against-display alone.
//
// The predicate names `legal_name` bare, with no coalesce. Postgres pairs an
// expression index by syntax, and idx_org_legal_name_trgm is built on the bare
// column — wrapping it here would silently cost a sequential scan of the whole
// table on every create. A NULL legal name makes its arm NULL rather than
// true, which is the same answer coalesce would have produced.
func fuzzyOrganization(ctx context.Context, tx pgx.Tx, c OrganizationCandidate) (OrganizationMatch, error) {
	args := []any{c.ExcludeID}
	arms := orgTrigramArms(&args, NormalizeOrgName(c.DisplayName), NormalizeOrgName(c.LegalName))
	if len(arms) == 0 {
		return OrganizationMatch{Decision: DecisionNoMatch}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, display_name, coalesce(legal_name, '') FROM organization
		 WHERE archived_at IS NULL
		   AND ($1::uuid IS NULL OR id <> $1)
		   AND (`+strings.Join(arms, " OR ")+`)`, args...)
	if err != nil {
		return OrganizationMatch{}, fmt.Errorf("dedupe org candidate set: %w", err)
	}
	defer rows.Close()

	var ranked []OrganizationCandidateScore
	for rows.Next() {
		var id ids.OrganizationID
		var name, rowLegal string
		if err := rows.Scan(&id, &name, &rowLegal); err != nil {
			return OrganizationMatch{}, fmt.Errorf("scan org candidate: %w", err)
		}
		score := bestOrgNamePairing(
			c.DisplayName, c.LegalName, name, rowLegal)
		if score.Confidence >= dedupeReviewThreshold {
			score.OrganizationID = id
			ranked = append(ranked, score)
		}
	}
	if err := rows.Err(); err != nil {
		return OrganizationMatch{}, fmt.Errorf("drain org candidates: %w", err)
	}
	if len(ranked) == 0 {
		return OrganizationMatch{Decision: DecisionNoMatch}, nil
	}
	// Confidence first, then the lowest id — a total order, so the queue does
	// not shuffle between runs.
	slices.SortFunc(ranked, func(a, b OrganizationCandidateScore) int {
		if a.Confidence != b.Confidence {
			return cmp.Compare(b.Confidence, a.Confidence)
		}
		return cmp.Compare(a.OrganizationID.String(), b.OrganizationID.String())
	})
	return OrganizationMatch{
		Decision:       DecisionFuzzyReview,
		OrganizationID: ranked[0].OrganizationID,
		Confidence:     ranked[0].Confidence,
		Ranked:         ranked,
	}, nil
}

// orgTrigramArms builds one `%` arm per (non-empty candidate axis × stored
// column), appending each value to args once.
//
// An EMPTY axis is dropped rather than passed as "". A `%` arm whose right
// side is empty matches nothing, but Postgres cannot satisfy it from the GIN
// index, so its presence makes the planner abandon the index for the whole OR
// group and sequentially scan the table — on the common case, since most
// organizations carry no registered name. Measured: two arms with a real value
// give a BitmapOr across both trigram indexes; adding the empty arms turns the
// same query into a Seq Scan.
func orgTrigramArms(args *[]any, axes ...string) []string {
	var arms []string
	for _, axis := range axes {
		if axis == "" {
			continue
		}
		*args = append(*args, axis)
		n := len(*args)
		for _, column := range []string{fieldDisplayName, fieldLegalName} {
			arms = append(arms, fmt.Sprintf(
				"f_fold_apostrophes(lower(%s)) %% f_fold_apostrophes(lower($%d))", column, n))
		}
	}
	return arms
}

// bestOrgNamePairing scores every pairing of the two name axes and reports
// which one won, with the raw values behind it so the queue can show the
// comparison that was actually made.
//
// An empty axis scores nothing rather than matching every other empty one: two
// companies that both lack a registered name have said nothing about being the
// same company.
func bestOrgNamePairing(candidateDisplay, candidateLegal, rowDisplay, rowLegal string) OrganizationCandidateScore {
	sides := []struct {
		value string
		field string
	}{{rowDisplay, fieldDisplayName}, {rowLegal, fieldLegalName}}

	var best OrganizationCandidateScore
	for _, left := range []string{candidateDisplay, candidateLegal} {
		for _, right := range sides {
			normalizedLeft, normalizedRight := NormalizeOrgName(left), NormalizeOrgName(right.value)
			if normalizedLeft == "" || normalizedRight == "" {
				continue
			}
			score := nameSimilarity(normalizedLeft, normalizedRight)
			if score > best.Confidence {
				best = OrganizationCandidateScore{
					Confidence:     score,
					MatchedField:   right.field,
					CandidateValue: left,
					IncumbentValue: right.value,
				}
			}
		}
	}
	return best
}
