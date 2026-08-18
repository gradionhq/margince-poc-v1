// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The channel: the companies the installation sells WITH.
//
// A partner is an ordinary organization carrying a partner extension row
// (A41/ADR-0032) — identity is never duplicated. So this phase writes the
// company the same way every other company is written, then PUTs the partner
// row onto it, which is what promotes it and what fills the Partners screen.
//
// It runs BEFORE the pipeline phases on purpose. Ownership walks every
// organization the installation holds, and an ownerless row is
// workspace-shared — visible at every row scope. A partner seeded after that
// pass would be the one company both SDRs could see.

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// partnerCounts is what one pass wrote. Organizations and promotions are
// counted apart because they converge differently: the company is probed and
// created once, while the partner row is an idempotent upsert that reports
// nothing about whether it already existed.
type partnerCounts struct {
	orgs, promoted, people, links int
}

// seedPartners files the partner companies, their staff, and the partner row
// that promotes each one.
func seedPartners(c *client, cfg demoConfig, mode runMode) (partnerCounts, error) {
	var n partnerCounts
	for _, p := range cfg.Partners {
		if p.Domain == "" || p.DisplayName == "" {
			return n, fmt.Errorf("partner %q: a partner needs both a domain and a display name", p.DisplayName)
		}
		if mode == modeDryRun {
			fmt.Printf("%-24s %-8s %s (partner)\n", p.Domain, outcomeDryRun, p.DisplayName)
			n.orgs++
			n.promoted++
			// Each person is employed at the partner that names them, so the
			// two counts move together — reporting people with no employments
			// would read as the bug this phase exists to fix.
			n.people += len(p.People)
			n.links += len(p.People)
			continue
		}
		orgID, existed, err := ensurePartnerOrganization(c, p)
		if err != nil {
			return n, fmt.Errorf("partner %s: %w", p.Domain, err)
		}
		if !existed {
			n.orgs++
		}
		fmt.Printf("%-24s %-8s %s (partner)\n", p.Domain, companyOutcome(existed, mode), p.DisplayName)

		if err := upsertPartnerRow(c, orgID, p); err != nil {
			return n, fmt.Errorf("promoting %s to a partner: %w", p.Domain, err)
		}
		n.promoted++

		staff, links, err := seedPartnerPeople(c, orgID, p)
		if err != nil {
			return n, fmt.Errorf("the staff at %s: %w", p.Domain, err)
		}
		n.people += staff
		n.links += links
	}
	return n, nil
}

// ensurePartnerOrganization finds the partner company by domain and creates it
// if absent — the same probe-then-create the crawled companies get, so a
// re-run adds nothing twice.
//
// It does NOT set relationship_types: the partner upsert below writes the
// live `partner` type itself, inside the transaction that writes the partner
// row (ADR-0079). Setting it here would be the same fact recorded twice, and
// the two could then disagree.
func ensurePartnerOrganization(c *client, p demoPartner) (id string, existed bool, err error) {
	if id, found, err := findOrganizationNamed(c, p.DisplayName); err != nil {
		return "", false, err
	} else if found {
		return id, true, nil
	}

	body := jsonBody{
		"display_name": p.DisplayName,
		"source":       seedSource,
		"domains":      []jsonBody{{"domain": p.Domain, "is_primary": true}},
	}
	addIfSet(body, "legal_name", p.LegalName)
	addIfSet(body, "industry", p.Industry)

	var out struct {
		ID string `json:"id"`
	}
	if err := c.post("/v1/organizations", body, &out); err != nil {
		if existing, ok := conflictingID(err); ok {
			return existing, true, nil
		}
		return "", false, err
	}
	return out.ID, false, nil
}

// upsertPartnerRow promotes the company. PUT /organizations/{id}/partner
// creates the row on first call and updates it after, so this converges
// without a probe.
//
// The If-Match precondition is deliberately omitted. It guards a human
// editing a partner against overwriting a change they did not see; the seeder
// has no prior version in hand and is restating the dataset's own values,
// which is the case the header exists to allow rather than to stop.
func upsertPartnerRow(c *client, orgID string, p demoPartner) error {
	body := jsonBody{"partner_role": p.PartnerRole}
	addIfSet(body, "cert_status", p.CertStatus)
	addIfSet(body, "margin_tier", p.MarginTier)
	addIfSet(body, "relationship_stage", p.RelationshipStage)
	addIfSet(body, "next_step", p.NextStep)
	return c.put("/v1/organizations/"+orgID+"/partner", body, nil)
}

// seedPartnerPeople files the partner's own staff and employs them there.
//
// The address is built here rather than carried in the dataset, which is the
// opposite of how a crawled person is handled — and for the opposite reason.
// A crawled person is real, so the dataset keeps the published and the
// synthesized address apart and the seeder never invents one. These people do
// not exist, and their company's domain is under .example, which RFC 2606
// reserves precisely so nothing can be delivered to it.
func seedPartnerPeople(c *client, orgID string, p demoPartner) (people, links int, err error) {
	for _, person := range p.People {
		email := partnerEmail(person.Name, p.Domain)
		if email == "" {
			continue
		}
		personID, existed, err := ensurePerson(c, datasetPers{Name: person.Name, Role: person.Role}, email, seedSource, false)
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
	return people, links, nil
}

// partnerEmail builds first.last@domain from a printed name.
//
// Diacritics are the norm rather than the exception here — two of the three
// partners are Vietnamese and Korean — so the name is folded to ASCII before
// it becomes a local part, and a name that folds to nothing yields no address
// rather than an address of punctuation.
func partnerEmail(name, domain string) string {
	first, last := splitName(name)
	parts := make([]string, 0, 2)
	for _, part := range []string{first, last} {
		if folded := asciiLocalPart(part); folded != "" {
			parts = append(parts, folded)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ".") + "@" + domain
}

// asciiLocalPart reduces one name word to the letters and digits an address
// may carry, lowercased. Anything else — a diacritic that did not decompose,
// a hyphen, a space — is dropped.
func asciiLocalPart(word string) string {
	var b strings.Builder
	// NFD splits an accented letter into its base plus a combining mark, so
	// dropping everything outside a-z0-9 below removes the mark and keeps the
	// letter: "Nguyễn" folds to "nguyen" rather than to "nguy".
	for _, r := range norm.NFD.String(strings.ToLower(word)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// findOrganizationNamed is findOrganization for a company that has no crawled
// read behind it — the search-then-match on display name, given the name
// directly.
func findOrganizationNamed(c *client, displayName string) (id string, found bool, err error) {
	var page struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	query := url.Values{"q": {displayName}, "limit": {"25"}}
	if err := c.get("/v1/organizations", query, &page); err != nil {
		return "", false, fmt.Errorf("searching for %q: %w", displayName, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.DisplayName, displayName) {
			return row.ID, true, nil
		}
	}
	return "", false, nil
}

// catalogueCounts is what the what-we-sell-and-how-we-file-it phases wrote.
type catalogueCounts struct {
	products, offers, surfaces, partnerEdges int
}

// seedCatalogue writes what the installation sells and the surfaces it files
// records under: the rate card, the quotes drawn from it, the tags, lists,
// custom fields, projects and quotas, and the edges tying each partner to the
// accounts it works on.
//
// The partner edges sit here rather than with the partner companies, which
// went in before the pipeline began: an edge needs the ids of BOTH ends, and
// the crawled accounts they point at are only indexed once refs is loaded.
func seedCatalogue(c *client, seats *sessions, cfg demoConfig, refs pipelineRefs, plan map[string]profile, mode runMode) (catalogueCounts, error) {
	var n catalogueCounts
	products, productsNew, err := seedProducts(c, cfg, mode)
	if err != nil {
		return n, err
	}
	n.products = productsNew
	if n.offers, err = seedOffers(c, cfg, refs, products, mode); err != nil {
		return n, err
	}
	if n.surfaces, err = seedSurfaces(c, seats, cfg, refs, plan, mode); err != nil {
		return n, err
	}
	if n.partnerEdges, err = seedPartnerEdges(c, cfg, refs, mode); err != nil {
		return n, err
	}
	return n, nil
}

// seedPartnerEdges files the org-to-org edges saying which accounts each
// partner works on: referred_by (the partner brought us the account),
// co_sell_with (we are selling into it together) and partner_of (the partner
// serves it).
//
// It runs after the pipeline refs are loaded, because it needs the ids of
// BOTH ends — the partners this file just wrote and the crawled accounts they
// point at — and refs.orgsByDom holds every organization the installation has.
//
// A relationship has no natural key the server refuses twice on, so each edge
// is probed before it is written. Without that a re-seed would file the same
// referral again on every run.
func seedPartnerEdges(c *client, cfg demoConfig, refs pipelineRefs, mode runMode) (int, error) {
	created := 0
	for i, e := range cfg.PartnerEdges {
		// A dry run wrote no partner companies, so neither end of the edge is
		// on file and neither can be resolved. Counting the edge and moving on
		// is the honest answer: the check below would otherwise report the
		// dry run's own restraint as a broken dataset.
		if mode == modeDryRun {
			created++
			continue
		}
		partnerID, ok := refs.orgsByDom[strings.ToLower(e.Partner)]
		if !ok {
			return created, fmt.Errorf("partner edge %d names partner %q, which is not seeded", i, e.Partner)
		}
		orgID, ok := refs.orgsByDom[strings.ToLower(e.Organization)]
		if !ok {
			return created, fmt.Errorf("partner edge %d names account %q, which is not seeded", i, e.Organization)
		}
		exists, err := partnerEdgeExists(c, e.Kind, orgID, partnerID)
		if err != nil {
			return created, fmt.Errorf("partner edge %d: %w", i, err)
		}
		if exists {
			continue
		}
		body := jsonBody{
			"kind":                e.Kind,
			"organization_id":     orgID,
			"counterparty_org_id": partnerID,
			"source":              seedSource,
		}
		if err := c.post("/v1/relationships", body, nil); err != nil {
			if isConflict(err) {
				continue
			}
			return created, fmt.Errorf("partner edge %d (%s %s -> %s): %w", i, e.Kind, e.Organization, e.Partner, err)
		}
		created++
	}
	return created, nil
}

// partnerEdgeExists asks whether this exact edge is already on file.
//
// The list filters by kind and by ONE organization; the counterparty is
// matched here, because an account may carry edges to several partners and
// the kind alone does not tell them apart.
func partnerEdgeExists(c *client, kind, orgID, partnerID string) (bool, error) {
	var page struct {
		Data []struct {
			CounterpartyOrgID string `json:"counterparty_org_id"`
		} `json:"data"`
	}
	query := url.Values{
		"kind":            {kind},
		"organization_id": {orgID},
		"limit":           {"100"},
	}
	if err := c.get("/v1/relationships", query, &page); err != nil {
		return false, fmt.Errorf("checking for an existing %s edge: %w", kind, err)
	}
	for _, row := range page.Data {
		if row.CounterpartyOrgID == partnerID {
			return true, nil
		}
	}
	return false, nil
}

func reportPartners(n partnerCounts) {
	fmt.Printf("partners:      %d organization(s) new, %d promoted, %d person/people, %d employment(s)\n",
		n.orgs, n.promoted, n.people, n.links)
}
