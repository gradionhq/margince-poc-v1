// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's legal-identity gate, split from the corpus call
// (sitecorpusread.go): the entity census becomes the multi-entity
// abstention verdict, and legal-trio fields keep their authority rules.

import (
	"net/url"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// legalWarningMultipleEntities is the abstention's user-facing warning —
// one spelling for the worker log, the debug report, and the E2E floor
// that greps it.
const legalWarningMultipleEntities = "disagreeing legal pages: the domain hosts more than one entity — the legal-field override was dropped"

// applyLegalGate turns the gated legal-entity census into the abstention
// verdict: more than one normalized-distinct entity → the whole legal
// trio is stripped (missing beats another company's); at most one → each
// trio field survives only when quoted from a shallow legal page.
func applyLegalGate(res corpusResult, idx corpusPages) ([]evidencedField, bool, []droppedFinding) {
	distinct := map[string]bool{}
	for _, e := range res.legalEntities {
		distinct[normalizeEvidence(e.Name)] = true
	}
	var dropped []droppedFinding
	if len(distinct) > 1 {
		kept := make([]evidencedField, 0, len(res.fields))
		for _, f := range res.fields {
			if legalPageFields[f.Field] {
				dropped = append(dropped, droppedFinding{
					Lane: laneLegal, Field: f.Field, Value: f.Value,
					EvidenceSnippet: f.EvidenceSnippet, Reason: dropLegalConflict,
				})
				continue
			}
			kept = append(kept, f)
		}
		return kept, true, dropped
	}
	kept := make([]evidencedField, 0, len(res.fields))
	for _, f := range res.fields {
		if legalPageFields[f.Field] &&
			(idx.kind[f.SourceURL] != crmcontracts.SiteReadPageKindImpressum || !legalAuthorityPage(f.SourceURL)) {
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
