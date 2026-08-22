// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Turning a profile into records.
//
// profile.go decides WHAT each company should have; this decides how to write
// it. Everything goes through the same endpoints and the same transitions
// demo.json's hand-authored records use — a generated won deal is closed by
// /advance like any other, and a generated lost deal carries a reason because
// the product requires one. Nothing here asserts a state the product would
// otherwise refuse.
//
// Companies demo.json names are skipped entirely: the planner marks them
// pinned and their records come from the dataset.

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// profileStageName maps a profile's stage to the workspace pipeline's own
// stage name. The pipeline is created at bootstrap with capitalised names
// (deals/pipeline.go:defaultStages) and the seeder matches stages BY NAME, so
// a lowercase value here would fail to resolve at run time rather than at
// compile time.
var profileStageName = map[string]string{
	"qualified":   "Qualified",
	"discovery":   "Discovery",
	"proposal":    "Proposal",
	"negotiation": "Negotiation",
	"won":         "Won",
	"lost":        "Lost",
}

// seedGeneratedDeals files a deal for every company whose profile calls for
// one, driving closed deals through the real /advance.
//
// It runs AFTER seedDeals so demo.json wins any collision: a pinned company
// is skipped here, and an existing deal with the same name is left alone.
func seedGeneratedDeals(c *client, cfg demoConfig, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	created := 0
	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || p.DealStage == "" {
			continue
		}
		orgID, ok := refs.orgsByDom[domain]
		if !ok {
			// A planned company the installation does not hold. Not an error:
			// -limit N seeds a subset, and the plan covers the whole dataset.
			continue
		}
		stageName, ok := profileStageName[p.DealStage]
		if !ok {
			return created, fmt.Errorf("profile for %s names deal stage %q, which is not a pipeline stage", domain, p.DealStage)
		}
		stageID, ok := refs.stagesByNm[stageName]
		if !ok {
			return created, fmt.Errorf("this pipeline has no stage %q (wanted by %s)", stageName, domain)
		}
		if mode == modeDryRun {
			created++
			continue
		}

		name := dealNameFor(localeFor(domain), refs.orgNameByID[orgID], p.DealStage)
		existing, err := findDeal(c, name, orgID)
		if err != nil {
			return created, err
		}
		if existing != "" {
			continue
		}

		// A deal is born in the first stage and moved, exactly as a real one
		// is: a closed deal cannot be created closed.
		openAt := stageID
		terminal := terminalStatus(stageName)
		if terminal != "" {
			openAt = refs.stagesByNm[refs.firstStage]
		}
		body := jsonBody{
			"name":            name,
			"pipeline_id":     refs.pipelineID,
			"stage_id":        openAt,
			"organization_id": orgID,
			"source":          seedSource,
			"amount_minor":    generatedAmount(domain),
			"currency":        currencyFor(localeFor(domain)),
		}
		if owner, ok := refs.usersByRef[refs.ownerRefByDomain[domain]]; ok {
			body["owner_id"] = owner
		}
		if terminal == "" {
			body["expected_close_date"] = refs.date(30 + hashIndex("close:"+domain, 120))
		}

		var out struct {
			ID string `json:"id"`
		}
		if err := c.post("/v1/deals", body, &out); err != nil {
			return created, fmt.Errorf("deal for %s: %w", domain, err)
		}
		created++

		if terminal == "" {
			continue
		}
		advance := jsonBody{"to_stage_id": stageID, "status": terminal}
		if terminal == "lost" {
			advance["lost_reason"] = p.LostReason
		}
		if terminal == "won" {
			// Every win needs this, contract planned or not: the contracts
			// phase runs after this one, so no paper is attached yet. See
			// wonWithoutContractReason.
			advance["won_without_contract_reason"] = wonWithoutContractReason
		}
		if err := c.post("/v1/deals/"+out.ID+"/advance", advance, nil); err != nil {
			return created, fmt.Errorf("closing deal for %s as %s: %w", domain, terminal, err)
		}
	}
	return created, nil
}

// generatedAmount is a plausible deal size, stable per company. Spread across
// a wide range so the pipeline's value column is not a flat line, and rounded
// to whole hundreds of euros because no real quote ends in 37 cents.
func generatedAmount(domain string) int64 {
	const (
		minHundreds = 40   // 4,000 EUR
		maxHundreds = 1800 // 180,000 EUR
	)
	hundreds := minHundreds + hashIndex("amount:"+domain, maxHundreds-minHundreds)
	// WHOLE EUROS, and it stays whole euros until a currency is chosen below.
	// This is the unit the range above is written in ("4,000 EUR"), and keeping
	// the figure in it is what stops the two scalings being applied to each
	// other.
	euros := int64(hundreds) * 100

	// Scaled into the account's own currency, because the figure is READ.
	// A dong contract carrying a euro-sized number printed "VND 154.800,00"
	// -- about 1,500 euro, which is not a number any Vietnamese contract
	// would show, and the rendered PDF is where that became obvious.
	//
	// Deliberately round multiples rather than the real FX rate: these are
	// plausible order sizes, not conversions of one another, and a demo whose
	// amounts track a rate invites a question nobody wants to answer.
	//
	// Each arm returns MINOR units of its own currency, which is the only place
	// the two scales meet: dong has none, so the rate alone is the answer, and
	// the euro and the dollar carry two. The previous version converted to euro
	// cents FIRST and then applied the dong rate to the cents, seeding a €4,000
	// deal as ₫10,000,000,000 — a hundred times its intent, and self-consistent
	// enough that the PDF looked plausible.
	switch currencyFor(localeFor(domain)) {
	case "VND":
		return euros * 25000 // money-scale-exempt: dong has no minor unit, so the rate IS the whole conversion
	case "USD":
		return euros * 100 // money-scale-exempt: minted at the dollar's two digits; near enough to the euro at this precision
	default:
		return euros * 100 // money-scale-exempt: minted at the euro's two digits
	}
}

// seedGeneratedLeads files the unqualified names at the top of the funnel for
// companies whose profile calls for one.
//
// The lead's person is invented rather than taken from the crawl: a lead is
// by definition somebody not yet in the CRM, and promoting one mints the
// person record. Using a real crawled contact would mean promoting them into
// a duplicate of themselves.
func seedGeneratedLeads(c *client, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	created := 0
	if mode == modeDryRun {
		for _, p := range plan {
			if !p.Pinned && p.LeadState != "" {
				created++
			}
		}
		return created, nil
	}

	existing, err := loadLeadsBySource(c)
	if err != nil {
		return 0, err
	}

	leadRank := leadAssignRank(plan)
	nameRank := leadNameRank(plan)

	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || p.LeadState == "" {
			continue
		}
		orgID, ok := refs.orgsByDom[domain]
		if !ok {
			continue
		}
		first, last := generatedLeadName(domain, nameRank[domain])
		title := generatedLeadTitle(domain)
		sourceID := "gen-lead-" + domain

		if existing[sourceID] == "" {
			body := jsonBody{
				"source":        seedSource,
				"source_system": "seed",
				"source_id":     sourceID,
				"status":        leadCreateStatus(p.LeadState),
				"full_name":     first + " " + last,
				"email":         generatedLeadEmail(first, last, domain),
				"company_name":  refs.orgNameByID[orgID],
				"title":         title,
			}
			// Half the generated leads are filed unassigned, which is what an
			// inbound funnel actually looks like: a lead arrives before
			// anybody picks it up. Seeding every one of them owned left the
			// queue-and-claim screens with nothing to show — there was no
			// unassigned lead to claim.
			//
			// Which half a lead falls in is a property of the DOMAIN, fixed
			// by leadAssignRank over the whole plan. See leadIsAssigned.
			//
			// The owner is looked up FIRST: a domain in the assigned half
			// whose owner cannot be resolved would otherwise be filed with no
			// owner_id and never repaired, because the existing-lead guard
			// above skips it on every later run.
			if owner, ok := refs.usersByRef[refs.ownerRefByDomain[domain]]; ok && leadIsAssigned(leadRank[domain]) {
				body["owner_id"] = owner
			}
			var out struct {
				ID string `json:"id"`
			}
			if err := c.post("/v1/leads", body, &out); err != nil {
				if _, conflict := conflictingID(err); !conflict {
					return created, fmt.Errorf("lead for %s: %w", domain, err)
				}
			} else {
				created++
				if err := driveLeadTo(c, out.ID, p.LeadState, domain); err != nil {
					return created, err
				}
			}
		}

		// The employment is ensured on EVERY run, not only when the lead is
		// created. Promotion mints a person with no employer, and a person
		// with no employer inherits no owner — so a run that promoted before
		// this repair existed would keep three contacts orphaned and
		// workspace-shared forever. ensureEmployment is a read-before-write,
		// so repeating it costs one request and changes nothing.
		if p.LeadState == "promoted" {
			if err := employPromotedPerson(c, first+" "+last, title, orgID); err != nil {
				return created, fmt.Errorf("employing the promoted lead for %s: %w", domain, err)
			}
		}
	}
	return created, nil
}

// driveLeadTo moves a freshly created lead to the state its profile asks for.
// Only promoted and disqualified need an action; the rest are creatable.
func driveLeadTo(c *client, leadID, state, domain string) error {
	switch state {
	case "promoted":
		// Promotion mints a person and archives the lead, so a re-run finds
		// the archived row by source_id and never repeats this.
		if err := c.post("/v1/leads/"+leadID+"/promote", jsonBody{"trigger": "human_qualify"}, nil); err != nil && !isConflict(err) {
			return fmt.Errorf("promoting lead for %s: %w", domain, err)
		}
	case "disqualified":
		// There is no /disqualify: DELETE is the operation, and it sets
		// status=disqualified and archives rather than removing the row.
		if err := c.delete("/v1/leads/" + leadID); err != nil && !isConflict(err) {
			return fmt.Errorf("disqualifying lead for %s: %w", domain, err)
		}
	}
	return nil
}

// leadCreateStatus is the status a lead may be CREATED with. Promoted and
// disqualified are reached by acting on the lead, never by asserting them —
// the API refuses both on create.
func leadCreateStatus(want string) string {
	switch want {
	case "contacted", "engaged":
		return want
	default:
		return "new"
	}
}

// generatedLeadName draws a name from the company's own naming culture.
//
// Names are assigned by RANK within each culture's pool, not by hashing the
// domain. Hashing gave the same pair to several companies -- "Tobias Ziegler"
// landed on four domains and "Kilian Wenzel" on nine before the pools grew --
// because a hash over 45 domains collides long before it exhausts a 20x20
// pool. Walking the pool in order makes every name distinct until the pool
// runs out, and the pools are bigger than the number of leads.
//
// The rank comes from the sorted domain list, so it is stable across runs for
// the same plan, exactly as leadAssignRank is.
func generatedLeadName(domain string, rank int) (string, string) {
	pool, ok := leadNamesByLocale[nameLocaleFor(domain)]
	if !ok {
		pool = leadNamesByLocale[namesDE]
	}
	// Stride the surnames so consecutive leads do not share one: with first
	// names cycling every len(First) entries, a plain rank would pair rank and
	// rank+len(First) with the same surname.
	first := pool.First[rank%len(pool.First)]
	last := pool.Last[(rank*7+rank/len(pool.First))%len(pool.Last)]
	return first, last
}

// generatedLeadEmail keeps the company in the LOCAL part, which is what makes
// the address unique.
//
// The obvious form, firstname.lastname@example.com, silently costs leads. The
// names come from an 8x8 pool, 40 domains hash into it with collisions, and
// the product rejects a second lead at an address it already holds — so a run
// created 16 leads instead of 46 and seeded no disqualified one at all, which
// the coverage matrix then failed on. Before the addresses moved to
// example.com the company's own domain had been supplying the uniqueness.
//
// The company slug is stripped of dots so the local part stays one label:
// jonas.sommer.shopify@example.com, not jonas.sommer.shopify.com@example.com.
func generatedLeadEmail(first, last, domain string) string {
	slug := strings.NewReplacer(".", "", "-", "").Replace(domain)
	return foldASCII(first) + "." + foldASCII(last) + "." + strings.ToLower(slug) + "@example.com"
}

// foldASCII turns a name into the local part a mail system would mint from it:
// Krüger -> krueger, Nguyễn -> nguyen, Ji-woo -> jiwoo.
//
// Necessary because the name pools are no longer German-only. Left unfolded,
// a Vietnamese lead was handed the address thảo.đỗ@example.com -- an address
// with combining marks in it, which is not what a mail system produces and not
// something an address validator has to accept.
//
// German umlauts EXPAND rather than dropping their diaeresis, because that is
// what German mail actually does: juettner@, not juttner@. Everything else
// decomposes and loses its marks, which covers Vietnamese and the Nordic and
// Slavic names in the list. This mirrors fold() in the dataset's
// tools/synth_emails.py, which does the same job for crawled people.
func foldASCII(name string) string {
	// D-with-stroke and the Nordic letters are distinct LETTERS, not a base
	// plus a combining mark, so NFKD leaves them alone and the alnum filter
	// then drops them: Đỗ became "o" and Yến Đinh became yen.inh. They have to
	// be mapped by hand.
	expanded := strings.NewReplacer(
		"ä", "ae", "ö", "oe", "ü", "ue",
		"Ä", "Ae", "Ö", "Oe", "Ü", "Ue",
		"ß", "ss",
		"đ", "d", "Đ", "D",
		"ø", "o", "Ø", "O", "å", "aa", "Å", "Aa", "æ", "ae", "Æ", "Ae",
		"ł", "l", "Ł", "L",
	).Replace(name)

	decomposed := norm.NFKD.String(expanded)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue // a combining mark: Nguyễn -> Nguyen
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r >= 'A' && r <= 'Z' {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func generatedLeadTitle(domain string) string {
	titles, ok := leadTitlesByLocale[nameLocaleFor(domain)]
	if !ok {
		titles = leadTitlesByLocale[namesDE]
	}
	return titles[hashIndex("leadtitle:"+domain, len(titles))]
}

// leadIsAssigned splits the generated leads in half: the assigned ones get
// the company's owner, the rest are left unassigned for somebody to claim.
//
// The split is by RANK, not by hash. A hash only promises "about half", and
// on the 45 domains that actually carry a generated lead every salt tried
// landed on 62/38 — a sample this small scatters. Taking every other domain
// gives exactly half.
func leadIsAssigned(rank int) bool {
	return rank%2 == 0
}

// leadAssignRank ranks every lead-bearing domain in the plan, so which half a
// lead falls in is a property of the DOMAIN rather than of the run.
//
// Ranking over the WHOLE plan is the point. An earlier version counted
// positions as the seeding loop walked them, which made the split depend on
// run history: `-limit N` plans a truncated company set, and a domain whose
// organization happens to be missing is skipped, so adding or removing an odd
// number of lead-bearing domains ahead of D flipped D into the other half.
// Leads already on file are never moved, so that left an installation's split
// depending on the order its runs happened in. The plan is the same on every
// run, so a rank taken from it is too.
// leadNameRank ranks each lead-bearing domain WITHIN its own naming culture,
// so a dataset with four Korean leads uses the first four Korean names.
//
// Ranked over the plan rather than counted as the seeding loop walks, for the
// same reason leadAssignRank is: the loop skips a domain whose organization
// the installation does not hold, so a `-limit N` run counted a different
// number of domains ahead of D than a full run did. A lead already on file is
// never renamed, so that left the NAME depending on the order the runs
// happened in -- and freed a name for another domain to take, which is how the
// duplicates this change exists to remove would have come back.
//
// The plan is identical on every run and knows nothing about which
// organizations exist, so a rank taken from it cannot drift.
func leadNameRank(plan map[string]profile) map[string]int {
	rank := make(map[string]int, len(plan))
	perCulture := map[nameLocale]int{}
	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || p.LeadState == "" {
			continue
		}
		culture := nameLocaleFor(domain)
		rank[domain] = perCulture[culture]
		perCulture[culture]++
	}
	return rank
}

func leadAssignRank(plan map[string]profile) map[string]int {
	rank := make(map[string]int, len(plan))
	n := 0
	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || p.LeadState == "" {
			continue
		}
		rank[domain] = n
		n++
	}
	return rank
}

// sortedDomains gives the plan a stable iteration order. Map order is random
// in Go, and a seeder that writes records in a different order every run
// produces a different audit trail for the same input.
func sortedDomains(plan map[string]profile) []string {
	out := make([]string, 0, len(plan))
	for domain := range plan {
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}
