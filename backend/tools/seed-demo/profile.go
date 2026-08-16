// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Deciding what each company gets, as a rule rather than a list.
//
// demo.json hand-writes the commercial story for five customers, which is
// right for them: their renewal chains and payment histories carry the demo's
// narrative and no generator would invent them well. It does not scale to the
// 200 companies behind them, and a demo where 195 accounts are identically
// empty is not test data.
//
// So every company that demo.json does NOT name gets a profile derived from
// its domain: a lifecycle, maybe a deal at some stage, maybe a contract,
// documents, a lead, a project. Two properties matter more than realism:
//
//   - STABLE. The assignment is a hash of the domain, so a company keeps its
//     profile across runs and across machines. A re-seed that reshuffled who
//     is a customer would make every screenshot and every bug report stale.
//   - COVERING. Weights alone leave rare states empty by luck. The planner
//     therefore fills the coverage matrix FIRST, promoting companies into
//     under-filled states, and only then applies realistic proportions.
//
// The order is what makes this useful for testing rather than only for demos.

import (
	"sort"
	"strings"
)

// profile is everything the seeder needs to know about one company beyond
// what the crawl found. Every field is an enum or a small count, so a profile
// is comparable, printable and cheap to assert against.
type profile struct {
	Domain string `json:"domain"`

	// Pinned marks a company demo.json names. Its records come from the
	// dataset and the planner leaves it alone, but it still COUNTS toward
	// coverage — the five story customers already supply several cells, and
	// promoting a sixth company to duplicate them would be waste.
	Pinned bool `json:"pinned"`

	Lifecycle string `json:"lifecycle"`
	// DealStage is "" for a company with no deal; otherwise a stage name, or
	// "won"/"lost" for a closed one.
	DealStage  string `json:"deal_stage,omitempty"`
	LostReason string `json:"lost_reason,omitempty"`
	// Contracts lists the status each contract should end in. More than one
	// means a chain — a superseded predecessor and its successor.
	Contracts []string `json:"contracts,omitempty"`
	// LooseDocs names account documents that belong to no contract.
	LooseDocs []string `json:"loose_docs,omitempty"`
	LeadState string   `json:"lead_state,omitempty"`
	Project   string   `json:"project,omitempty"`
}

// hasContract reports whether any contract is planned, which is what decides
// whether a won deal needs a won_without_contract_reason.
func (p profile) hasContract() bool { return len(p.Contracts) > 0 }

// axisValues is the profile as coverage cells, so counting is one function
// rather than one per axis.
func (p profile) axisValues() map[coverageAxis][]string {
	out := map[coverageAxis][]string{}
	if p.Lifecycle != "" {
		out[axisLifecycle] = []string{p.Lifecycle}
	}
	if p.DealStage != "" {
		out[axisDeal] = []string{p.DealStage}
	}
	if len(p.Contracts) > 0 {
		out[axisContract] = append([]string(nil), p.Contracts...)
		out[axisDocument] = []string{"contract_pdf"}
	}
	if len(p.LooseDocs) > 0 {
		out[axisDocument] = append(out[axisDocument], "loose")
	}
	if p.LeadState != "" {
		out[axisLead] = []string{p.LeadState}
	}
	if p.Project != "" {
		out[axisProject] = []string{p.Project}
	}
	return out
}

// The realistic proportions, as hash buckets out of 100. A company lands in
// the first band its hash falls into, so the shares are exact rather than
// approximate and do not drift as companies are added.
var lifecycleBands = []struct {
	Value string
	Share int
}{
	{"customer", 10},
	{"opportunity", 10},
	{"prospect", 22},
	{"former_customer", 5},
	{"target", 53},
}

// dealStageBands is what an opportunity's deal looks like. Only companies
// that HAVE a deal draw from this.
var dealStageBands = []struct {
	Value string
	Share int
}{
	{"qualified", 25},
	{"discovery", 30},
	{"proposal", 25},
	{"negotiation", 20},
}

// lostReasons are the closed-lost reasons, which must all appear or the
// reason filter has nothing to filter. Kept here rather than in demo.json
// because the coverage matrix asserts against them.
var lostReasons = []string{
	"price",
	"lost to competitor",
	"no budget this year",
	"no decision",
}

var looseDocTypes = []string{"nda", "price_list", "dpa", "order_form"}

// planProfiles decides what every company gets.
//
// companies is every accepted domain; cfg supplies the pinned ones. The result
// is keyed by lowercase domain and is fully determined by those two inputs —
// no clock, no randomness, no ordering dependence.
func planProfiles(domains []string, cfg demoConfig) map[string]profile {
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)

	pinned := pinnedDomains(cfg)
	out := make(map[string]profile, len(sorted))

	// Pass 1: the base assignment, at realistic weights.
	for _, domain := range sorted {
		domain = strings.ToLower(domain)
		if _, ok := pinned[domain]; ok {
			// A named company's records come from demo.json. The profile
			// exists only so it counts toward coverage.
			out[domain] = profile{Domain: domain, Pinned: true, Lifecycle: pinned[domain]}
			continue
		}
		out[domain] = baseProfile(domain)
	}

	// Pass 2: promote companies until the matrix is satisfied.
	fillCoverage(sorted, pinned, out)
	return out
}

// baseProfile is one company's profile from weights alone, before coverage.
func baseProfile(domain string) profile {
	p := profile{Domain: domain}
	p.Lifecycle = pickBand(domain, "lifecycle", lifecycleBands)

	switch p.Lifecycle {
	case "customer":
		p.DealStage = "won"
		p.Contracts = []string{"active"}
		p.Project = pickOne(domain, "project", []string{"delivering", "delivering", "closed"})
	case "former_customer":
		p.DealStage = "won"
		p.Contracts = []string{"expired"}
	case "opportunity":
		p.DealStage = pickBand(domain, "dealstage", dealStageBands)
	case "prospect":
		// A prospect has been contacted. Some have a lead on file, some a
		// deal that went nowhere.
		if hashIndex("prospectlost:"+domain, 3) == 0 {
			p.DealStage = "lost"
			p.LostReason = lostReasons[hashIndex("lostreason:"+domain, len(lostReasons))]
		} else {
			p.LeadState = pickOne(domain, "lead", []string{"new", "working"})
		}
	case "target":
		// Mostly nothing, which is the honest majority. A few carry an
		// untouched lead.
		if hashIndex("targetlead:"+domain, 5) == 0 {
			p.LeadState = "new"
		}
	}

	if hashIndex("loosedoc:"+domain, 6) == 0 {
		p.LooseDocs = []string{looseDocTypes[hashIndex("doctype:"+domain, len(looseDocTypes))]}
	}
	return p
}

// fillCoverage promotes companies into under-filled cells until the matrix is
// satisfied, or until it runs out of companies to promote.
//
// Stability under insert is the hard requirement: adding a company next month
// must not reshuffle everybody. Promotion therefore walks candidates in an
// order derived from the CELL and the domain — not from the iteration order
// and not from a counter — so a newcomer either outranks an existing promotee
// for one cell or changes nothing at all.
func fillCoverage(domains []string, pinned map[string]string, out map[string]profile) {
	targets := coverageTarget(planningMatrix())

	// Lifecycle is settled BEFORE anything else, because promoting a company's
	// lifecycle rewrites its contracts and project — a customer must own a
	// contract, a target must not. Filling contracts first and lifecycle
	// second silently undid the contract work, which is what left
	// contract=draft at 2 of 3 with 24 eligible candidates sitting unused.
	for _, cell := range orderedCells() {
		for {
			counts := countCoverage(out)
			if counts[cell.Axis][cell.Value] >= targets[cell.Axis][cell.Value] {
				break
			}
			candidate := bestCandidate(domains, pinned, out, cell)
			if candidate == "" {
				// Not enough companies to satisfy this cell. Verify reports
				// it; the planner does not invent a company to fix it.
				break
			}
			out[candidate] = promote(out[candidate], cell)
		}
	}
}

// bestCandidate is the company that should take this cell next: the one whose
// promotion costs the least, ranked stably by hash so the choice never depends
// on map order.
//
// "Costs the least" means: prefer a company that is not already covering a
// scarce cell on the same axis. A company is skipped entirely when it is
// pinned (demo.json owns it) or already holds this value.
func bestCandidate(domains []string, pinned map[string]string, out map[string]profile, cell coverageCell) string {
	best, bestRank := "", -1
	for _, domain := range domains {
		domain = strings.ToLower(domain)
		if _, isPinned := pinned[domain]; isPinned {
			continue
		}
		p := out[domain]
		if holds(p, cell) || !promotable(p, cell) {
			continue
		}
		// Do not rob a cell to fill another. Lifecycle, deal stage, lead state
		// and project are SINGLE-valued on a profile, so promoting a company
		// that currently holds a value overwrites it — and if that value was
		// already scarce, the net coverage does not improve. This is what left
		// project=initiative at 1 of 2: the same company was picked for
		// `initiative` and then again for `delivering`, which erased the first.
		if wouldStarve(out, cell, p) {
			continue
		}
		// A large stable spread per (cell, domain): the same company is
		// picked for the same cell on every machine, and a different one for
		// a different cell.
		rank := hashIndex(string(cell.Axis)+":"+cell.Value+":"+domain, 1<<20)
		if rank > bestRank {
			best, bestRank = domain, rank
		}
	}
	return best
}

// singleValuedAxes are the axes where a profile holds exactly one value, so
// writing a new one destroys the old. Contracts and documents are lists and
// can accumulate, which is why they are absent here.
var singleValuedAxes = map[coverageAxis]bool{
	axisLifecycle: true,
	axisDeal:      true,
	axisLead:      true,
	axisProject:   true,
}

// wouldStarve reports whether promoting this company would push some OTHER
// cell below its minimum — taking from one pocket to fill another.
func wouldStarve(out map[string]profile, cell coverageCell, p profile) bool {
	if !singleValuedAxes[cell.Axis] {
		return false
	}
	current := p.axisValues()[cell.Axis]
	if len(current) == 0 {
		return false
	}
	counts := countCoverage(out)
	targets := coverageTarget(planningMatrix())
	for _, value := range current {
		// Losing this value must not drop the cell it belongs to below its
		// floor. A value with no floor at all is free to overwrite.
		if counts[cell.Axis][value]-1 < targets[cell.Axis][value] {
			return true
		}
	}
	return false
}

// holds reports whether a profile already covers a cell.
func holds(p profile, cell coverageCell) bool {
	for _, value := range p.axisValues()[cell.Axis] {
		if value == cell.Value {
			return true
		}
	}
	return false
}

// promotable refuses promotions that would contradict the company's story.
//
// A contract belongs to somebody who bought something, and a project to
// somebody being delivered to. Promoting a target into "active contract"
// would satisfy the matrix and produce a record no reader would believe.
func promotable(p profile, cell coverageCell) bool {
	switch cell.Axis {
	case axisContract:
		switch cell.Value {
		case "active", "superseded":
			return p.Lifecycle == "customer"
		case "expired", "cancelled":
			return p.Lifecycle == "customer" || p.Lifecycle == "former_customer"
		case "draft":
			// Paper gets drafted before anyone signs, so an open deal at any
			// stage is enough — a draft contract on a qualified deal is an
			// ordinary state, not a contradiction. Narrowing this to
			// negotiation-or-later left too few candidates to fill the cell.
			return p.DealStage != "" && p.DealStage != "lost"
		}
	case axisProject:
		// Delivery work belongs to somebody who bought, but a project can be
		// an INITIATIVE before the deal closes — that is what the phase means.
		if cell.Value == "initiative" {
			return p.Lifecycle == "opportunity" || p.Lifecycle == "customer"
		}
		return p.Lifecycle == "customer" || p.Lifecycle == "former_customer"
	case axisDeal:
		switch cell.Value {
		case "won":
			return p.Lifecycle == "customer" || p.Lifecycle == "former_customer"
		case "lost":
			return p.Lifecycle == "prospect" || p.Lifecycle == "target"
		default:
			// An open deal makes the account an opportunity at least.
			return p.Lifecycle == "opportunity" || p.Lifecycle == "prospect" || p.Lifecycle == "target"
		}
	case axisLead:
		// A lead is a name at the top of the funnel — it does not fit an
		// account that is already a customer.
		return p.Lifecycle != "customer" && p.Lifecycle != "former_customer"
	case axisDocument, axisLifecycle:
		return true
	}
	return true
}

// promote gives a profile the cell's value, adjusting whatever else must
// change for the result to stay coherent.
func promote(p profile, cell coverageCell) profile {
	switch cell.Axis {
	case axisLifecycle:
		p.Lifecycle = cell.Value
		// The lifecycle carries obligations: a customer bought something.
		switch cell.Value {
		case "customer":
			p.DealStage, p.LeadState = "won", ""
			if len(p.Contracts) == 0 {
				p.Contracts = []string{"active"}
			}
		case "former_customer":
			p.DealStage, p.LeadState = "won", ""
			if len(p.Contracts) == 0 {
				p.Contracts = []string{"expired"}
			}
		case "opportunity":
			if p.DealStage == "" || p.DealStage == "won" || p.DealStage == "lost" {
				p.DealStage = pickBand(p.Domain, "dealstage", dealStageBands)
			}
			p.Contracts = nil
		case "target", "prospect":
			p.Contracts, p.Project = nil, ""
			if p.DealStage == "won" {
				p.DealStage = ""
			}
		}
	case axisDeal:
		p.DealStage = cell.Value
		if cell.Value == "lost" && p.LostReason == "" {
			p.LostReason = lostReasons[hashIndex("lostreason:"+p.Domain, len(lostReasons))]
		}
		if cell.Value != "lost" {
			p.LostReason = ""
		}
	case axisContract:
		if cell.Value == "superseded" {
			// Superseded only exists as the front half of a renewal: the old
			// agreement plus the one that replaced it. Both are PREPENDED
			// rather than replacing the slice — overwriting it destroyed
			// contracts an earlier cell had already placed here, which left
			// contract=draft short while eligible companies went unused.
			p.Contracts = append([]string{"superseded", "active"}, p.Contracts...)
		} else {
			p.Contracts = append(p.Contracts, cell.Value)
		}
	case axisLead:
		p.LeadState = cell.Value
	case axisProject:
		p.Project = cell.Value
	case axisDocument:
		if cell.Value == "loose" && len(p.LooseDocs) == 0 {
			p.LooseDocs = []string{looseDocTypes[hashIndex("doctype:"+p.Domain, len(looseDocTypes))]}
		}
		if cell.Value == "contract_pdf" && len(p.Contracts) == 0 {
			p.Contracts = []string{"active"}
		}
	}
	return p
}

// countCoverage tallies every profile across every axis.
func countCoverage(profiles map[string]profile) map[coverageAxis]map[string]int {
	counts := map[coverageAxis]map[string]int{}
	for _, p := range profiles {
		for axis, values := range p.axisValues() {
			if counts[axis] == nil {
				counts[axis] = map[string]int{}
			}
			for _, value := range values {
				counts[axis][value]++
			}
		}
	}
	return counts
}

// pinnedDomains is every company demo.json names, mapped to the lifecycle the
// dataset gives it. Those companies are the seeder's authored half and the
// planner never overrides them.
func pinnedDomains(cfg demoConfig) map[string]string {
	pinned := map[string]string{}
	note := func(domain string) {
		if domain != "" {
			pinned[strings.ToLower(domain)] = ""
		}
	}
	for _, deal := range cfg.Deals {
		note(deal.Company)
	}
	for _, contract := range cfg.Contracts {
		note(contract.Company)
	}
	for _, act := range cfg.Activities {
		note(act.Company)
	}
	for _, domain := range cfg.FinanceCustomers {
		note(domain)
	}
	// demo.json's lifecycle map is authoritative for the companies it names,
	// including ones with no other records.
	for lifecycle, domains := range cfg.Lifecycle {
		for _, domain := range domains {
			pinned[strings.ToLower(domain)] = lifecycle
		}
	}
	return pinned
}

// pickBand chooses a value by weighted hash. Shares are out of 100 and the
// last band absorbs any rounding, so every input lands somewhere.
func pickBand(domain, salt string, bands []struct {
	Value string
	Share int
},
) string {
	roll := hashIndex(salt+":"+domain, 100)
	acc := 0
	for _, band := range bands {
		acc += band.Share
		if roll < acc {
			return band.Value
		}
	}
	return bands[len(bands)-1].Value
}

// pickOne chooses one of a list by hash. Repeat a value to weight it.
func pickOne(domain, salt string, options []string) string {
	if len(options) == 0 {
		return ""
	}
	return options[hashIndex(salt+":"+domain, len(options))]
}
