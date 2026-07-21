// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's legal-identity gate: the entity census becomes the
// multi-entity abstention verdict, and legal-trio fields keep their
// authority rules — only a shallow legal page testifies, and a disputed
// entity means no legal identity is proposed at all.

import (
	"net/url"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// corpusLegalEntity is one entity a legal page names, with the identity
// details printed alongside it. It is both the census the multi-entity
// abstention counts AND, when that abstention fires, the choice offered to
// the human: a group's imprint lists its subsidiaries in blocks — name,
// registered address, registration or VAT number — and dropping all of it
// because there were five leaves a person retyping what the page already
// stated, which is the one thing this read exists to prevent.
type corpusLegalEntity struct {
	Name string `json:"name"`
	// RegisteredAddress and RegisterNumber are empty when the page states
	// the entity but not that detail — never guessed to fill the block.
	RegisteredAddress string `json:"registered_address,omitempty"`
	RegisterNumber    string `json:"register_number,omitempty"`
	EvidenceSnippet   string `json:"evidence_snippet,omitempty"`
	SourceURL         string `json:"source_url"`
}

// dedupeLegalEntities folds the census into the list a human is offered.
// One entity reaches it several times: every locale of the legal page
// states it, and a group's page labels each block with a market name
// ("Gradion Singapur") above the entity that trades there. A register
// number is the identity a registry issues, so blocks sharing one are the
// same company however they are labelled; entities without one fall back
// to their normalized name. The richest sighting wins, so a locale that
// printed the address is not lost to one that omitted it.
func dedupeLegalEntities(entities []corpusLegalEntity) []corpusLegalEntity {
	var out []corpusLegalEntity
	for _, entity := range entities {
		at := matchingLegalEntity(out, entity)
		if at < 0 {
			out = append(out, entity)
			continue
		}
		if legalEntityDetail(entity) > legalEntityDetail(out[at]) {
			out[at] = entity
		}
	}
	return out
}

// matchingLegalEntity joins locale variants without collapsing genuinely
// distinct registrations. A translated legal page can omit the register
// number that another locale printed; those sightings still match by name.
// When both sightings carry different register numbers, the registry
// identities win and the entities remain separate even if their names match.
func matchingLegalEntity(existing []corpusLegalEntity, candidate corpusLegalEntity) int {
	candidateName := normalizeEvidence(candidate.Name)
	candidateRegister := normalizeEvidence(candidate.RegisterNumber)
	for i, entity := range existing {
		name := normalizeEvidence(entity.Name)
		register := normalizeEvidence(entity.RegisterNumber)
		sameRegister := candidateRegister != "" && register != "" && candidateRegister == register
		compatibleName := candidateName != "" && candidateName == name &&
			(candidateRegister == "" || register == "" || candidateRegister == register)
		if sameRegister || compatibleName {
			return i
		}
	}
	return -1
}

// legalEntityDetail counts how much of an entity block was actually
// printed — the tie-break when the same entity is seen twice.
func legalEntityDetail(entity corpusLegalEntity) int {
	filled := 0
	for _, value := range []string{entity.RegisteredAddress, entity.RegisterNumber} {
		if strings.TrimSpace(value) != "" {
			filled++
		}
	}
	return filled
}

// legalWarningMultipleEntities is the abstention's user-facing warning —
// one spelling for the worker log, the debug report, and the E2E floor
// that greps it.
const legalWarningMultipleEntities = "disagreeing legal pages: the domain hosts more than one entity — the legal-field override was dropped"

// applyLegalGate turns the gated legal-entity census into the abstention
// verdict: more than one normalized-distinct entity → the whole legal
// trio is stripped (missing beats another company's); at most one → each
// trio field survives only when quoted from a shallow legal page.
func applyLegalGate(fields []evidencedField, entities []corpusLegalEntity, pageKind map[string]crmcontracts.SiteReadPageKind, censusIncomplete bool) ([]evidencedField, bool, []droppedFinding) {
	distinct := map[string]bool{}
	for _, e := range entities {
		distinct[normalizeEvidence(e.Name)] = true
	}
	var dropped []droppedFinding
	if censusIncomplete || len(distinct) > 1 {
		reason := dropLegalConflict
		if censusIncomplete && len(distinct) <= 1 {
			reason = dropLegalCensusIncomplete
		}
		kept := make([]evidencedField, 0, len(fields))
		for _, f := range fields {
			if legalPageFields[f.Field] {
				dropped = append(dropped, droppedFinding{
					Lane: laneLegal, Field: f.Field, Value: f.Value,
					EvidenceSnippet: f.EvidenceSnippet, Reason: reason,
				})
				continue
			}
			kept = append(kept, f)
		}
		return kept, true, dropped
	}
	kept := make([]evidencedField, 0, len(fields))
	for _, f := range fields {
		if legalPageFields[f.Field] &&
			(pageKind[f.SourceURL] != crmcontracts.SiteReadPageKindImpressum || !legalAuthorityPage(f.SourceURL)) {
			dropped = append(dropped, droppedFinding{
				Lane: laneLegal, Field: f.Field, Value: f.Value,
				EvidenceSnippet: f.EvidenceSnippet, Reason: dropLegalNotFromLegalPage,
			})
			continue
		}
		kept = append(kept, f)
	}
	return kept, false, dropped
}

// legalAuthorityPage limits legal-identity authority to legal pages the
// site OPERATOR plausibly owns: at most two path segments deep
// (/impressum, /de/impressum). Anything deeper reads like content ABOUT
// a legal page — a customer's imprint, an archived copy — and must not
// speak for the company.
func legalAuthorityPage(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	depth := 0
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment != "" {
			depth++
		}
	}
	return depth <= 2
}
