// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's cross-page fold for the page-parallel lane: facts
// dedupe on factKey, people on the normalized name, entities union.
// With the binary citation gate there is no model confidence to break
// ties — page-kind specificity does (an Impressum's phone beats a
// homepage mention), then first-seen, and page order is deterministic,
// so the merge is too.

import (
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// factPageRank orders page kinds by how specifically they state a
// single-value fact: the Impressum legally states contact identity, a
// contact page deliberately publishes it, catalog pages state their own
// items, about and home pages merely mention things.
var factPageRank = map[crmcontracts.SiteReadPageKind]int{
	crmcontracts.SiteReadPageKindImpressum: 6,
	crmcontracts.SiteReadPageKindContact:   5,
	crmcontracts.SiteReadPageKindServices:  4,
	crmcontracts.SiteReadPageKindProducts:  4,
	crmcontracts.SiteReadPageKindTeam:      3,
	crmcontracts.SiteReadPageKindAbout:     2,
	crmcontracts.SiteReadPageKindHome:      1,
}

// mergePageResults folds the per-page findings into one result set:
// single-value facts take the most-specific page kind's answer,
// multi-value facts keep the first (most-specific-first would churn
// value spellings without adding truth), people dedupe on the
// normalized name keeping the more specific page's entry, entities
// union — the abstention needs every voice.
func mergePageResults(results []pageFactsResult) pageFactsResult {
	var out pageFactsResult
	factIndex := map[string]int{}
	factRank := map[string]int{}
	personIndex := map[string]int{}
	personRank := map[string]int{}
	for _, res := range results {
		rank := factPageRank[res.kind]
		for _, fact := range res.facts {
			key := factKey(fact)
			at, seen := factIndex[key]
			if !seen {
				factIndex[key] = len(out.facts)
				factRank[key] = rank
				out.facts = append(out.facts, fact)
				continue
			}
			if fact.ValueKey == "" && rank > factRank[key] {
				out.facts[at] = fact
				factRank[key] = rank
			}
		}
		for _, person := range res.people {
			key := normalizedPersonName(person.Name)
			at, seen := personIndex[key]
			if !seen {
				personIndex[key] = len(out.people)
				personRank[key] = rank
				out.people = append(out.people, person)
				continue
			}
			if rank > personRank[key] {
				out.people[at] = person
				personRank[key] = rank
			}
		}
		out.entities = append(out.entities, res.entities...)
	}
	return out
}
