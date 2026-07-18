// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's cross-page merge, split from the job spine
// (deepread.go): how per-page findings fold into one answer per field,
// and the wrong-company guards that temper the legal page's authority.

import (
	"net/url"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// pageFields pairs one crawled page's identity with its gate-surviving
// findings: the shared company fields, the page kind's category facts,
// and — on team pages — the published people.
type pageFields struct {
	url    string
	kind   crmcontracts.SiteReadPageKind
	fields []evidencedField
	facts  []people.DeepReadFact
	people []sitePerson
}

// mergeCrawlFields folds the per-page extractions into one answer per
// field with the same pairwise rule the quick read applies
// (mergeSiteFields): impressum-kind pages accumulate as the legal side,
// every other page in crawl order as the seed side, and the final merge
// lets the legal side win exactly the legal trio. Deterministic: crawl
// order is deterministic and each fold step is.
//
// Two wrong-company guards temper the legal side's authority:
//   - Only a SHALLOW legal page (legalAuthorityPage) joins it. A legal
//     notice buried deep in the path (/customers/acme/legal) is likelier
//     someone else's than the site operator's; it still extracts, but as
//     an ordinary page with no override power.
//   - When two authority pages DISAGREE on the legal name, the domain
//     hosts more than one entity and no override can be trusted: the
//     legal side is discarded wholesale (legalConflict true) — missing
//     legal fields beat the wrong company's.
func mergeCrawlFields(pages []pageFields) (merged []evidencedField, legalConflict bool) {
	var seed, legal []evidencedField
	legalNames := map[string]string{} // normalized legal_name → first spelling
	for _, p := range pages {
		if p.kind == crmcontracts.SiteReadPageKindImpressum && legalAuthorityPage(p.url) {
			legal = mergeSiteFields(legal, p.fields)
			for _, f := range p.fields {
				if f.Field == string(crmcontracts.LegalName) {
					legalNames[normalizeEvidence(f.Value)] = f.Value
				}
			}
			continue
		}
		if p.kind == crmcontracts.SiteReadPageKindImpressum {
			// A deep (non-authority) legal page: fill-only. Even inside the
			// seed fold, mergeSiteFields would hand it the legal trio —
			// exactly the override the depth rule denies it.
			seed = fillMissingFields(seed, p.fields)
			continue
		}
		seed = mergeSiteFields(seed, p.fields)
	}
	if len(legalNames) > 1 {
		// Full abstention, not just a dropped override: with the entity
		// in dispute, a legal_name quoted off a marketing page is as
		// untrustworthy as the losing legal page's — the read proposes NO
		// legal identity and the human resolves it.
		return withoutLegalTrio(seed), true
	}
	return mergeSiteFields(seed, legal), false
}

// withoutLegalTrio strips the legal-identity fields from a field set —
// the multi-entity abstention applied to whatever side survives.
func withoutLegalTrio(fields []evidencedField) []evidencedField {
	kept := fields[:0]
	for _, f := range fields {
		if !legalPageFields[f.Field] {
			kept = append(kept, f)
		}
	}
	return kept
}

// fillMissingFields appends only the fields `have` lacks — no override
// of any kind, whatever the field.
func fillMissingFields(have, extra []evidencedField) []evidencedField {
	present := map[string]bool{}
	for _, f := range have {
		present[f.Field] = true
	}
	for _, f := range extra {
		if !present[f.Field] {
			present[f.Field] = true
			have = append(have, f)
		}
	}
	return have
}

// legalAuthorityPage limits the legal-trio override to legal pages the
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
