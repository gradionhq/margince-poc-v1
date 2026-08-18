// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Leads dedupe against LEADS (ADR-0118/A169 §2): the exact keys — email and
// LinkedIn URL — already answer 409 at create, so what is left for the review
// queue is the near-match a key cannot catch: the same person keyed in twice
// under two spellings, or once with an address and once without. Detection
// reads the lead's own columns and NEVER the person table; a lead is proposed
// as a duplicate of a lead or of nothing.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// entityLead is the dedupe_candidate discriminator for a lead pair.
const entityLead = "lead"

// leadEmailColumn is the lead's exact-key address column.
const leadEmailColumn = "email"

// identityTouched answers whether a patch changed any of the named columns.
func identityTouched(p *storekit.Patch, columns ...string) bool {
	after := p.After()
	for _, column := range columns {
		if _, changed := after[column]; changed {
			return true
		}
	}
	return false
}

// leadCandidateRow is one live lead the fuzzy query returned.
type leadCandidateRow struct {
	id       ids.LeadID
	fullName *string
	company  *string
	domain   string
}

// leadNearMatch is the best fuzzy neighbour of a lead, or none.
type leadNearMatch struct {
	found      bool
	id         ids.LeadID
	confidence float64
	incumbent  string
}

// recordLeadNearMatch leaves the review trail a lead write owes: when a live
// lead reads like this one, the pair goes on the queue with the evidence the
// detector saw. It runs inside the caller's transaction so the lead and its
// review trail commit or roll back together. A lead with no name has nothing
// to be near — the exact keys are its whole identity, and those are 409s.
func (s *Store) recordLeadNearMatch(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead, by string) error {
	if deref(lead.FullName) == "" {
		return nil
	}
	match, err := s.fuzzyLead(ctx, tx, lead)
	if err != nil {
		return err
	}
	if !match.found {
		return nil
	}
	return recordNearMatch(ctx, tx, entityLead, ids.UUID(lead.Id), match.id.UUID, match.confidence,
		nearMatchEvidence(fieldFullName, deref(lead.FullName), match.incumbent, match.confidence), lead.Source, by)
}

// fuzzyLead is the lead-internal PO-F-1 analogue: name similarity carries the
// person weight, and the "same employer" term is answered from what a lead
// has — the free-text company name, or a shared non-consumer mail domain —
// since a lead holds no organization edge by design.
func (s *Store) fuzzyLead(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead) (leadNearMatch, error) {
	consumerMail, err := s.consumerMailMatcher(ctx, tx)
	if err != nil {
		return leadNearMatch{}, err
	}
	name := deref(lead.FullName)
	company := strings.TrimSpace(deref(lead.CompanyName))
	domain := ""
	if lead.Email != nil {
		domain = emailDomain(string(*lead.Email))
		if consumerMail.IsConsumer(domain) {
			// A shared consumer domain says nothing about a shared employer.
			domain = ""
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id, full_name, company_name, coalesce(split_part(email, '@', 2), '')
		  FROM lead
		 WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND archived_at IS NULL AND id <> $1
		   AND (f_fold_apostrophes(lower(coalesce(full_name, ''))) % f_fold_apostrophes(lower($2))
		        OR ($3 <> '' AND lower(company_name) = lower($3))
		        OR ($4 <> '' AND lower(split_part(email, '@', 2)) = $4))`,
		lead.Id, name, company, domain)
	if err != nil {
		return leadNearMatch{}, fmt.Errorf("dedupe lead candidate set: %w", err)
	}
	defer rows.Close()

	best := leadNearMatch{}
	for rows.Next() {
		var row leadCandidateRow
		if err := rows.Scan(&row.id, &row.fullName, &row.company, &row.domain); err != nil {
			return leadNearMatch{}, fmt.Errorf("scan lead candidate: %w", err)
		}
		confidence := leadConfidence(name, company, domain, row)
		// Equal confidence resolves to the lowest id — a total order, so the
		// queue does not shuffle between runs.
		if confidence > best.confidence ||
			(confidence == best.confidence && best.found && row.id.String() < best.id.String()) {
			best = leadNearMatch{found: true, id: row.id, confidence: confidence, incumbent: deref(row.fullName)}
		}
	}
	if err := rows.Err(); err != nil {
		return leadNearMatch{}, fmt.Errorf("drain lead candidates: %w", err)
	}
	if !best.found || best.confidence < dedupeReviewThreshold {
		return leadNearMatch{}, nil
	}
	return best, nil
}

// leadConfidence weights name similarity and the employer term exactly as
// PO-F-1 does for a person, so a lead pair and a person pair at the same
// confidence mean the same thing to the reviewer.
func leadConfidence(name, company, domain string, row leadCandidateRow) float64 {
	employer := 0.0
	switch {
	case company != "" && row.company != nil && strings.EqualFold(company, strings.TrimSpace(*row.company)):
		employer = 1.0
	case domain != "" && normalizeDomain(row.domain) == domain:
		employer = 0.8
	}
	return dedupeNameWeight*nameSimilarity(name, deref(row.fullName)) + dedupeOrgDomainWeight*employer
}
