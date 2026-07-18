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

// corpusLegalEntity is one entity a legal page names — the census the
// multi-entity abstention counts.
type corpusLegalEntity struct {
	Name      string `json:"name"`
	SourceURL string `json:"source_url"`
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
