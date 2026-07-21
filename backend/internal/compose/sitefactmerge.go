// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's cross-page fold for the page-parallel lane: facts
// dedupe on category+field+value key, people on the normalized name,
// entities union.
// With the binary citation gate there is no model confidence to break
// ties — page-kind specificity does (an Impressum's phone beats a
// homepage mention), then first-seen, and page order is deterministic,
// so the merge is too.

import (
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
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

// A fact's identity within one read is the identity the DB and the
// selection API already use: category + field + value key (factKey). Two
// findings that share a NAME but not a field are two facts — a company
// legitimately appears as both a partner and a named customer, and
// collapsing them would silently drop one of its own claims.
//
// Consumers that select by value key alone (the onboarding wizard's
// selected_fact_keys) therefore have to fold the duplicates themselves; a
// set of keys is what they send, and one key selects every fact carrying
// it.

// factBands curate a read down to what a human will actually confirm.
//
// Truncating the merged list instead would be quietly wrong: facts arrive
// in crawl-commit order, the probes (imprint, about, contact) and the
// partner wall commit first, and the offering pages commit last — so a
// plain head-of-list cut keeps thirty-five integration partners and
// twenty office cities while dropping what the company sells, which is
// the one thing company context exists to record.
//
// So the budget is split by what each kind of fact is worth to a CRM,
// richest band first, and a band that cannot fill its share lends the
// remainder to the next. Within a band the merge's own order survives, so
// the result stays deterministic.
//
// Named offerings band separately from capabilities on purpose. A leaf
// page states its bullets as capabilities — "zero-downtime deployment
// strategies", "segregation of duties within deployment pipelines" — and
// there are always more of those than a company has things to sell. Share
// one band and the bullets crowd out the catalog.
var factBands = []struct {
	quota  int
	fields []string
}{
	{40, []string{people.FactProduct, people.FactService}},                                                                          // what the company sells, by name
	{10, []string{people.FactCapability}},                                                                                           // how it delivers
	{25, []string{people.FactNamedCustomer, people.FactCertification, people.FactQuantifiedOutcome}},                                // what proves it
	{10, []string{people.FactServedIndustry, people.FactCompanySize, people.FactGeography, people.FactLanguage}},                    // who it sells to
	{10, []string{people.FactTechnology, people.FactPartner}},                                                                       // what it builds on
	{5, []string{people.FactLocation, people.FactFoundedYear, people.FactEmployeeRange, people.FactPhone, people.FactContactEmail}}, // who it is
}

// capFacts applies the bands under the API's own bound
// (identity.MaxSelectedFacts) rather than a number of this package's
// choosing: the confirm step preselects every fact a read returned, so a
// read allowed to exceed it would build a request the server refuses. It
// is the honest UX limit too — a review step that asks someone to vet
// three hundred claims collects a rubber stamp, not a confirmation.
func capFacts(facts []people.DeepReadFact) []people.DeepReadFact {
	if len(facts) <= identity.MaxSelectedFacts {
		return facts
	}
	bandOf := map[string]int{}
	for band, spec := range factBands {
		for _, field := range spec.fields {
			bandOf[field] = band
		}
	}
	// A field no band claims still competes, in the last band — a new fact
	// field must lose its slot to a known one, never vanish unreviewed.
	unbanded := len(factBands) - 1

	keep := make([]bool, len(facts))
	kept, spare := 0, 0
	for band, spec := range factBands {
		budget := spec.quota + spare
		taken := 0
		for i, fact := range facts {
			if kept == identity.MaxSelectedFacts || taken == budget {
				break
			}
			at, known := bandOf[fact.Field]
			if !known {
				at = unbanded
			}
			if at == band {
				keep[i] = true
				taken++
				kept++
			}
		}
		spare = budget - taken
	}
	// Lending only flows forward, so a shortfall in the LAST band — or in
	// any band no later one could spend — would leave the page short while
	// unreviewed facts remain. Fill what is left in merge order: a partly
	// filled budget is a worse read, never a safer one.
	for i := range facts {
		if kept == identity.MaxSelectedFacts {
			break
		}
		if !keep[i] {
			keep[i] = true
			kept++
		}
	}
	out := make([]people.DeepReadFact, 0, kept)
	for i, fact := range facts {
		if keep[i] {
			out = append(out, fact)
		}
	}
	return out
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
	out.facts = capFacts(out.facts)
	return out
}
