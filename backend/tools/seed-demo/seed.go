// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The seeding pass: one organization per reviewed company, one person per
// name that company's own website published, and an employment tying them
// together.
//
// Every write probes first, so running twice creates nothing twice and
// running after the dataset grows creates only what it gained. That is what
// makes "extend the dataset and re-seed" the supported workflow rather than
// "wipe and rebuild".

import (
	"fmt"
	"net/url"
	"strings"
)

// seedSource marks every row this tool writes. `source` is client-suppliable;
// `captured_by` is stamped by the server from the authenticated principal and
// is never ours to set.
const seedSource = "seed:demo"

type counts struct {
	orgsCreated, orgsExisting     int
	peopleCreated, peopleExisting int
	links, skipped                int
}

// seedPipeline runs the phases that need companies and people to exist
// first: leads and deals, what happened on them, what was signed and quoted,
// and who consented to what.
func seedPipeline(c *client, seats *sessions, cfg demoConfig, companies []company, refs pipelineRefs, mode runMode) error {
	// What each company beyond demo.json's named few should hold. Decided
	// once, from the domains alone, so every phase below agrees about which
	// company is a customer and which is an untouched target.
	domains := make([]string, 0, len(companies))
	for _, comp := range companies {
		domains = append(domains, strings.ToLower(comp.Domain))
	}
	plan := planProfiles(domains, cfg)

	// FX first: winning a deal freezes its rate, so a non-EUR deal cannot
	// close until one exists. Korean companies bill in USD and Vietnamese in
	// dong, so this is load-bearing rather than tidy.
	fxRates, err := seedFxRates(c, refs, plan, mode)
	if err != nil {
		return err
	}
	leads, err := seedLeads(c, cfg, refs, mode)
	if err != nil {
		return err
	}
	generatedLeads, err := seedGeneratedLeads(c, refs, plan, mode)
	if err != nil {
		return err
	}
	deals, err := seedDeals(c, cfg, refs, mode)
	if err != nil {
		return err
	}
	// Generated deals come AFTER the dataset's own, so a company demo.json
	// names keeps the deal the story gives it and the planner adds nothing.
	generatedDeals, err := seedGeneratedDeals(c, cfg, refs, plan, mode)
	if err != nil {
		return err
	}
	// The deals have to be on file before anything can point at them: an
	// activity links to the deal it moved, and a stakeholder sits on one.
	if err := refs.loadDeals(c, cfg); err != nil {
		return err
	}
	// The invented staff go in BEFORE the committees are drawn. A deal's
	// stakeholders come from its company's employees, and seedStakeholders
	// skips a company that publishes nobody — so staffing these five after it
	// left them the only staffed companies whose deals had no committee, which
	// is exactly what the verify rule catches.
	inventedPeople, inventedLinks, err := seedInventedStaff(c, cfg, refs, mode)
	if err != nil {
		return err
	}
	stakeholders, err := seedStakeholders(c, cfg, refs, mode)
	if err != nil {
		return err
	}
	activities, err := seedActivities(c, seats, cfg, refs, mode)
	if err != nil {
		return err
	}
	paper, err := seedPaper(c, cfg, refs, plan, mode)
	if err != nil {
		return err
	}
	catalogue, err := seedCatalogue(c, seats, cfg, refs, plan, mode)
	if err != nil {
		return err
	}
	consents, err := seedConsent(c, cfg, companies, refs, mode)
	if err != nil {
		return err
	}
	lifecycles, err := seedLifecycle(c, cfg, refs, plan, mode)
	if err != nil {
		return err
	}
	standing, err := seedWhatEachCompanyIs(c, cfg, refs, mode)
	if err != nil {
		return err
	}
	// Ownership runs last: it walks every organization the installation holds,
	// including any a previous run created, so it must see the finished set.
	ownedOrgs, ownedPeople, err := assignOwners(c, cfg, refs, mode)
	if err != nil {
		return err
	}

	reportPipeline(pipelineCounts{
		leads: leads, generatedLeads: generatedLeads,
		deals: deals, generatedDeals: generatedDeals,
		stakeholders: stakeholders, activities: activities,
		contracts: paper.contracts, generatedContracts: paper.generatedContracts,
		products: catalogue.products, offers: catalogue.offers,
		documents: paper.documents, looseDocs: paper.looseDocs,
		consents: consents, lifecycles: lifecycles, surfaces: catalogue.surfaces, fxRates: fxRates,
		partnerEdges: catalogue.partnerEdges, relTypes: standing.relTypes,
		dualPartners: standing.dualPartners, inventedPeople: inventedPeople, inventedLinks: inventedLinks,
		ownedOrgs: ownedOrgs, ownedPeople: ownedPeople,
	})
	return nil
}

// paperCounts is what the agreements-and-documents phases wrote.
type paperCounts struct {
	contracts, generatedContracts int
	documents, looseDocs          int
}

// seedPaper files the agreements and the documents that hang off them.
//
// Order matters twice here. The dataset's contracts come before the generated
// ones so a company demo.json names keeps the story's own agreement. And the
// contract PDFs come after BOTH, because seedDocuments walks every contract
// the installation holds — which is what gets the generated ones their paper
// with no extra work.
func seedPaper(c *client, cfg demoConfig, refs pipelineRefs, plan map[string]profile, mode runMode) (paperCounts, error) {
	var n paperCounts
	var err error
	if n.contracts, err = seedContracts(c, cfg, refs, mode); err != nil {
		return n, err
	}
	if n.generatedContracts, err = seedGeneratedContracts(c, refs, plan, mode); err != nil {
		return n, err
	}
	if n.documents, err = seedDocuments(c, refs, mode); err != nil {
		return n, err
	}
	if n.looseDocs, err = seedLooseDocuments(c, refs, plan, mode); err != nil {
		return n, err
	}
	return n, nil
}

// pipelineCounts is what one pass created, split by whether the dataset asked
// for it or the planner did — the two answer different questions when a
// number looks wrong.
type pipelineCounts struct {
	leads, generatedLeads         int
	deals, generatedDeals         int
	stakeholders, activities      int
	contracts, generatedContracts int
	products, offers              int
	documents, looseDocs          int
	consents, lifecycles          int
	surfaces, fxRates             int
	partnerEdges, relTypes        int
	dualPartners                  int
	inventedPeople, inventedLinks int
	ownedOrgs, ownedPeople        int
}

func reportPipeline(n pipelineCounts) {
	fmt.Printf("leads:         %d new (%d from demo.json, %d generated)\n", n.leads+n.generatedLeads, n.leads, n.generatedLeads)
	fmt.Printf("deals:         %d new (%d from demo.json, %d generated)\n", n.deals+n.generatedDeals, n.deals, n.generatedDeals)
	fmt.Printf("stakeholders:  %d new\n", n.stakeholders)
	fmt.Printf("activities:    %d new\n", n.activities)
	fmt.Printf("contracts:     %d new (%d from demo.json, %d generated)\n", n.contracts+n.generatedContracts, n.contracts, n.generatedContracts)
	fmt.Printf("products:      %d new\n", n.products)
	fmt.Printf("offers:        %d new\n", n.offers)
	fmt.Printf("documents:     %d uploaded (%d contract PDFs, %d account documents)\n", n.documents+n.looseDocs, n.documents, n.looseDocs)
	fmt.Printf("fx rates:      %d loaded\n", n.fxRates)
	fmt.Printf("surfaces:      %d new (tags, lists, custom fields, projects, quotas)\n", n.surfaces)
	fmt.Printf("partner edges: %d new (referrals, co-sells, served accounts)\n", n.partnerEdges)
	fmt.Printf("rel. types:    %d set (what each company IS to us)\n", n.relTypes)
	fmt.Printf("dual partners: %d customer(s) also promoted to partner\n", n.dualPartners)
	fmt.Printf("invented staff:%d person/people, %d employment(s) — companies that publish none\n", n.inventedPeople, n.inventedLinks)
	fmt.Printf("consent:       %d recorded\n", n.consents)
	fmt.Printf("lifecycle:     %d changed\n", n.lifecycles)
	fmt.Printf("owners:        %d organization(s), %d person/people\n", n.ownedOrgs, n.ownedPeople)
}

func seed(c *client, companies []company, dryRun bool) error {
	var total counts
	for _, comp := range companies {
		got, err := seedCompany(c, comp, dryRun)
		if err != nil {
			return fmt.Errorf("%s: %w", comp.Domain, err)
		}
		total.orgsCreated += got.orgsCreated
		total.orgsExisting += got.orgsExisting
		total.peopleCreated += got.peopleCreated
		total.peopleExisting += got.peopleExisting
		total.links += got.links
		total.skipped += got.skipped
	}

	if dryRun {
		fmt.Printf("\nDRY RUN — nothing was written.\n")
	}
	fmt.Printf("\norganizations: %d new, %d already present\n", total.orgsCreated, total.orgsExisting)
	fmt.Printf("people:        %d new, %d already present", total.peopleCreated, total.peopleExisting)
	if total.skipped > 0 {
		fmt.Printf(", %d skipped for having no address", total.skipped)
	}
	fmt.Printf("\nemployments:   %d new\n", total.links)
	return nil
}

func seedCompany(c *client, comp company, dryRun bool) (counts, error) {
	var got counts

	orgID, existed, err := ensureOrganization(c, comp, dryRun)
	if err != nil {
		return got, err
	}
	if existed {
		got.orgsExisting++
	} else {
		got.orgsCreated++
	}
	fmt.Printf("%-24s %-8s %s\n", comp.Domain, companyOutcome(existed, modeFor(dryRun)), comp.displayName())

	for _, person := range comp.People {
		email, _ := person.email()
		if email == "" {
			// Nobody to file: a contact with no address is not usable in a
			// demo, and inventing one HERE would bypass the dataset's own
			// rule about where synthesized addresses come from.
			got.skipped++
			continue
		}
		personID, existed, err := ensurePerson(c, person, email, seedSource, dryRun)
		if err != nil {
			return got, fmt.Errorf("person %q: %w", person.Name, err)
		}
		if existed {
			got.peopleExisting++
		} else {
			got.peopleCreated++
		}
		if orgID == "" || personID == "" {
			continue // dry run: nothing to link
		}
		linked, err := ensureEmployment(c, personID, orgID, person.Role, dryRun)
		if err != nil {
			return got, fmt.Errorf("employment for %q: %w", person.Name, err)
		}
		if linked {
			got.links++
		}
	}
	return got, nil
}

// outcome is what happened to one record, for the per-company line.
type outcome string

const (
	outcomeNew      outcome = "new"
	outcomeExisting outcome = "exists"
	outcomeDryRun   outcome = "(dry)"
)

func companyOutcome(existed bool, mode runMode) outcome {
	switch {
	case mode == modeDryRun:
		return outcomeDryRun
	case existed:
		return outcomeExisting
	default:
		return outcomeNew
	}
}

// runMode says whether this pass writes. It is a named type rather than a
// bool so it cannot be swapped with the "did this already exist?" flag beside
// it at a call site.
type runMode int

const (
	modeWrite runMode = iota
	modeDryRun
)

func modeFor(dryRun bool) runMode {
	if dryRun {
		return modeDryRun
	}
	return modeWrite
}

// ensureOrganization finds the company by domain and creates it if absent.
// The domain is the natural key: it is what the crawl was keyed on, and two
// companies sharing one is the merge case, not a seeding case.
func ensureOrganization(c *client, comp company, dryRun bool) (id string, existed bool, err error) {
	if id, found, err := findOrganization(c, comp); err != nil {
		return "", false, err
	} else if found {
		return id, true, nil
	}
	if dryRun {
		return "", false, nil
	}

	body := jsonBody{
		"display_name": comp.displayName(),
		"source":       seedSource,
		"domains":      []jsonBody{{"domain": comp.Domain, "is_primary": true}},
	}
	addIfSet(body, "legal_name", comp.value("legal_name"))
	addIfSet(body, "industry", comp.value("industry"))
	if description := comp.value("offer_summary"); description != "" {
		body["description"] = truncate(description, 500)
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := c.post("/v1/organizations", body, &out); err != nil {
		// The duplicate-domain refusal NAMES the record it collided with, so
		// a second run resolves the company from the server's own answer.
		// That is more reliable than re-probing: search is
		// eventually-consistent behind an index, and a company created
		// moments ago is exactly the case it has not caught up with.
		if existing, ok := conflictingID(err); ok {
			return existing, true, nil
		}
		return "", false, err
	}
	return out.ID, false, nil
}

func findOrganization(c *client, comp company) (id string, found bool, err error) {
	var page struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	query := url.Values{"q": {comp.displayName()}, "limit": {"25"}}
	if err := c.get("/v1/organizations", query, &page); err != nil {
		return "", false, fmt.Errorf("searching for %s: %w", comp.Domain, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.DisplayName, comp.displayName()) {
			return row.ID, true, nil
		}
	}
	return "", false, nil
}

// ensurePerson finds someone by their address and creates them if absent.
// The address is the natural key the product itself dedupes on.
//
// source says where this person came from, and is a parameter rather than a
// constant because the answer is load-bearing: almost everyone here was read
// off their employer's own website, while the twelve invented for companies
// that publish no staff carry inventedPersonSource. A query must always be
// able to tell the two apart.
func ensurePerson(c *client, person datasetPers, email, source string, dryRun bool) (id string, existed bool, err error) {
	if id, found, err := findPerson(c, email); err != nil {
		return "", false, err
	} else if found {
		return id, true, nil
	}
	if dryRun {
		return "", false, nil
	}

	first, last := splitName(person.Name)
	body := jsonBody{
		"full_name": person.Name,
		"source":    source,
		"emails":    []jsonBody{{"email": email, "email_type": "work", "is_primary": true}},
	}
	addIfSet(body, "first_name", first)
	addIfSet(body, "last_name", last)
	addIfSet(body, "title", person.Role)

	var out struct {
		ID string `json:"id"`
	}
	if err := c.post("/v1/people", body, &out); err != nil {
		if existing, ok := conflictingID(err); ok {
			return existing, true, nil
		}
		return "", false, err
	}
	return out.ID, false, nil
}

func findPerson(c *client, email string) (id string, found bool, err error) {
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	query := url.Values{"q": {email}, "limit": {"5"}}
	if err := c.get("/v1/people", query, &page); err != nil {
		return "", false, fmt.Errorf("searching for %s: %w", email, err)
	}
	if len(page.Data) == 0 {
		return "", false, nil
	}
	return page.Data[0].ID, true, nil
}

// ensureEmployment ties a person to the company whose site named them.
//
// Unlike organizations and people, a relationship has no natural key the
// server refuses twice on — POSTing the same employment again simply creates
// a second edge. So this one is probed rather than attempted-and-recovered,
// which is what keeps a re-run from filing everybody at their employer over
// and over.
func ensureEmployment(c *client, personID, orgID, role string, dryRun bool) (created bool, err error) {
	if dryRun {
		return false, nil
	}
	if employed, err := alreadyEmployed(c, personID, orgID); err != nil {
		return false, err
	} else if employed {
		return false, nil
	}
	body := jsonBody{
		"kind":               "employment",
		"person_id":          personID,
		"organization_id":    orgID,
		"is_current_primary": true,
		"source":             seedSource,
	}
	addIfSet(body, "role", role)

	if err := c.post("/v1/relationships", body, nil); err != nil {
		if isConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func alreadyEmployed(c *client, personID, orgID string) (bool, error) {
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	query := url.Values{
		"kind":            {"employment"},
		"person_id":       {personID},
		"organization_id": {orgID},
		"limit":           {"1"},
	}
	if err := c.get("/v1/relationships", query, &page); err != nil {
		return false, fmt.Errorf("checking employment: %w", err)
	}
	return len(page.Data) > 0, nil
}

func addIfSet(body jsonBody, key, value string) {
	if value != "" {
		body[key] = value
	}
}

// truncate cuts to a rune budget the contract enforces, so an over-long
// description is shortened here rather than refused there.
func truncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max-1]) + "…"
}
