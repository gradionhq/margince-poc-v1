// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What each company IS to us, and the two demo states that needed inventing:
// a company that is a customer and a partner at once, and staff for the
// customers whose own sites publish none.

import (
	"fmt"
	"sort"
	"strings"
)

// inventedPersonSource marks a person nobody's website published.
//
// A different source from seedSource on purpose: 780 people in this
// installation were read off a real company's own site, twelve were invented
// because their employer publishes no staff directory, and no query should
// have to guess which is which.
const inventedPersonSource = "seed:invented"

// relationshipTypesFor decides what one company is to us.
//
// A rule with a short exception list, not a list: the dataset grows, and a
// company ingested next month must land with a type without anyone editing
// anything. lifecycle already says where an account stands, and for all but a
// handful of companies that answers what it is as well — a customer is a
// customer, and a company nobody has sold to yet is `other` rather than a
// pretend supplier.
//
// The overrides carry what the rule cannot know: that a company also resells
// for us, that one is a vendor we buy from, that a lost account is now a
// competitor.
func relationshipTypesFor(domain, lifecycle string, cfg demoConfig) []string {
	if types, ok := cfg.RelTypes.Overrides[strings.ToLower(domain)]; ok && len(types) > 0 {
		return types
	}
	switch lifecycle {
	case "customer", "former_customer":
		return []string{"customer"}
	default:
		return []string{"other"}
	}
}

// seedRelationshipTypes fills the multi-valued "what is this company to us"
// on every organization.
//
// It runs AFTER seedLifecycle, because the type is derived from the stage.
// Before this existed only the three synthetic partners carried a type and
// the other 187 companies carried none, so the field the company header
// renders was empty on every real account.
//
// A company that already carries `partner` keeps it. The type and the partner
// row are two halves of one invariant (ADR-0079) and removing the type while
// the row lives is refused with 422 — so a replace-set that forgot it would
// not quietly drop the partner, it would fail the seed.
func seedRelationshipTypes(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	changed := 0
	domains := make([]string, 0, len(refs.orgsByDom))
	for domain := range refs.orgsByDom {
		domains = append(domains, domain)
	}
	// Sorted so a re-run walks the same order and a failure names the same
	// company twice rather than a different one each time.
	sort.Strings(domains)

	for _, domain := range domains {
		if mode == modeDryRun {
			changed++
			continue
		}
		orgID := refs.orgsByDom[domain]
		current, err := organizationTypes(c, orgID)
		if err != nil {
			return changed, fmt.Errorf("reading %s: %w", domain, err)
		}
		want := withPartnerKept(relationshipTypesFor(domain, current.Lifecycle, cfg), current.Types)
		if sameTypes(current.Types, want) {
			continue
		}
		body := jsonBody{"relationship_types": want, "if_version": current.Version}
		if err := c.patch("/v1/organizations/"+orgID, body, nil); err != nil {
			return changed, fmt.Errorf("typing %s as %s: %w", domain, strings.Join(want, "+"), err)
		}
		changed++
	}
	return changed, nil
}

// withPartnerKept adds `partner` to the desired set when the record already
// carries it. relationship_types is a REPLACE-set, so a rule that computed
// "customer" for a company that is also a partner would be asking the server
// to drop the partner type — which it refuses (422) while the partner row
// lives, because the two are one invariant.
func withPartnerKept(want, current []string) []string {
	for _, t := range current {
		if t != "partner" {
			continue
		}
		for _, w := range want {
			if w == "partner" {
				return want
			}
		}
		return append(append([]string{}, want...), "partner")
	}
	return want
}

// sameTypes compares two type sets ignoring order, so a re-run that would
// write the same answer writes nothing.
func sameTypes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func organizationTypes(c *client, orgID string) (struct {
	Lifecycle string   `json:"lifecycle"`
	Types     []string `json:"relationship_types"`
	Version   int      `json:"version"`
}, error) {
	var out struct {
		Lifecycle string   `json:"lifecycle"`
		Types     []string `json:"relationship_types"`
		Version   int      `json:"version"`
	}
	err := c.get("/v1/organizations/"+orgID, nil, &out)
	return out, err
}

// seedDualRolePartners promotes real crawled companies that are already
// customers.
//
// This is the case ADR-0079 exists for: "the partner program is built on
// companies that are simultaneously partners and customers". The synthetic
// <Country>Partner three prove the Partners screen renders; this proves a
// partner need not be a separate record from the customer.
//
// It reuses the same upsert the synthetic partners take, so there is one
// promotion path rather than two that can drift.
func seedDualRolePartners(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	promoted := 0
	for i, dual := range cfg.DualRolePartners {
		orgID, ok := refs.orgsByDom[strings.ToLower(dual.Company)]
		if !ok {
			return promoted, fmt.Errorf("dual-role partner %d names company %q, which is not seeded", i, dual.Company)
		}
		if mode == modeDryRun {
			promoted++
			continue
		}
		err := upsertPartnerRow(c, orgID, demoPartner{
			PartnerRole:       dual.PartnerRole,
			CertStatus:        dual.CertStatus,
			MarginTier:        dual.MarginTier,
			RelationshipStage: dual.RelationshipStage,
			NextStep:          dual.NextStep,
		})
		if err != nil {
			return promoted, fmt.Errorf("promoting %s to a partner: %w", dual.Company, err)
		}
		promoted++
	}
	return promoted, nil
}

// seedInventedStaff files people for real companies that publish none.
//
// The dataset's locked rule is that real people come only from the website
// reader, and this is its one stated exception. It is confined to five Asian
// customers that carry signed contracts and no contacts: the crawl is right
// to have found nobody — Korean and Vietnamese manufacturers publish products
// and certifications, not staff directories — but a demo that opens the
// account, finds the paper and then finds an empty Contacts tab reads as a
// broken product rather than an honest one.
//
// Everyone written here is marked with inventedPersonSource and sits on a
// .example address, so they can never be mistaken for, or mailed as, one of
// the 780 the reader actually found.
func seedInventedStaff(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (people, links int, err error) {
	for i, entry := range cfg.InventedStaff {
		orgID, ok := refs.orgsByDom[strings.ToLower(entry.Company)]
		if !ok {
			return people, links, fmt.Errorf("invented staff %d names company %q, which is not seeded", i, entry.Company)
		}
		for _, person := range entry.People {
			if mode == modeDryRun {
				people++
				links++
				continue
			}
			email := partnerEmail(person.Name, inventedDomain(entry.Company))
			if email == "" {
				return people, links, fmt.Errorf("invented staff at %s: %q yields no address", entry.Company, person.Name)
			}
			personID, existed, err := ensurePerson(c,
				datasetPers{Name: person.Name, Role: person.Role}, email, inventedPersonSource, false)
			if err != nil {
				return people, links, fmt.Errorf("person %q: %w", person.Name, err)
			}
			if !existed {
				people++
			}
			linked, err := ensureEmployment(c, personID, orgID, person.Role, false)
			if err != nil {
				return people, links, fmt.Errorf("employment for %q: %w", person.Name, err)
			}
			if linked {
				links++
			}
		}
	}
	return people, links, nil
}

// inventedDomain is the undeliverable domain an invented person's address
// sits on: the company's own domain with its dots folded into hyphens, under
// .example.
//
// NOT the company's real domain. These people do not work there in any sense
// a mail server would agree with, and an address at the real domain would be
// deliverable to whoever does read that mailbox.
func inventedDomain(companyDomain string) string {
	slug := strings.NewReplacer(".", "-").Replace(strings.ToLower(companyDomain))
	return slug + ".example"
}

// standingCounts is what the what-each-company-IS phases wrote.
type standingCounts struct {
	relTypes, dualPartners int
}

// seedWhatEachCompanyIs settles what every company is to us, as against where
// it stands in the funnel — the two halves ADR-0079 split apart.
//
// The promotion comes BEFORE the typing, and the order is load-bearing. An org
// IS a partner iff it carries the `partner` type AND has a partner row, and
// the server enforces that in both directions: adding the type to a company
// with no row is refused (422 `partner_row_missing`) exactly as removing the
// type from one that has a row is. Typing bestit.de first is what stopped an
// earlier seed dead.
func seedWhatEachCompanyIs(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (standingCounts, error) {
	var n standingCounts
	var err error
	if n.dualPartners, err = seedDualRolePartners(c, cfg, refs, mode); err != nil {
		return n, err
	}
	// What a company IS follows where it STANDS, so this reads the stage
	// seedLifecycle settled.
	if n.relTypes, err = seedRelationshipTypes(c, cfg, refs, mode); err != nil {
		return n, err
	}
	return n, nil
}
