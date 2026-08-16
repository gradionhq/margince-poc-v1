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

// generatedDealName is what a generated deal is called. It names the company
// so a board full of them still reads as a list of accounts rather than a
// column of identical rows.
func generatedDealName(displayName, stage string) string {
	switch stage {
	case "won":
		return displayName + " — Erstvertrag"
	case "lost":
		return displayName + " — Evaluierung"
	default:
		return displayName + " — Einführung"
	}
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

		name := generatedDealName(refs.orgNameByID[orgID], p.DealStage)
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
			"currency":        "EUR",
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
	return int64(hundreds) * 100 * 100 // hundreds of euros, in minor units
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

	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || p.LeadState == "" {
			continue
		}
		orgID, ok := refs.orgsByDom[domain]
		if !ok {
			continue
		}
		first, last := generatedLeadName(domain)
		title := generatedLeadTitle(domain)
		sourceID := "gen-lead-" + domain

		if existing[sourceID] == "" {
			body := jsonBody{
				"source":        seedSource,
				"source_system": "seed",
				"source_id":     sourceID,
				"status":        leadCreateStatus(p.LeadState),
				"full_name":     first + " " + last,
				"email":         strings.ToLower(first+"."+last) + "@" + domain,
				"company_name":  refs.orgNameByID[orgID],
				"title":         title,
			}
			if owner, ok := refs.usersByRef[refs.ownerRefByDomain[domain]]; ok {
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
	case "working":
		return "working"
	default:
		return "new"
	}
}

var leadFirstNames = []string{"Jonas", "Mareike", "Tobias", "Svenja", "Kilian", "Annika", "Fabian", "Carla"}
var leadLastNames = []string{"Wenzel", "Achterberg", "Roth", "Lindqvist", "Sommer", "Baumgart", "Reinhold", "Kessler"}
var leadTitles = []string{
	"Head of E-Commerce", "Digital Product Owner", "Leiter IT", "Marketing Manager",
	"Head of Operations", "Projektleiterin Digitalisierung",
}

func generatedLeadName(domain string) (string, string) {
	first := leadFirstNames[hashIndex("leadfirst:"+domain, len(leadFirstNames))]
	last := leadLastNames[hashIndex("leadlast:"+domain, len(leadLastNames))]
	return first, last
}

func generatedLeadTitle(domain string) string {
	return leadTitles[hashIndex("leadtitle:"+domain, len(leadTitles))]
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
