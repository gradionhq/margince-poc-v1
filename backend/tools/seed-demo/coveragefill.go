// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Filling the coverage matrix: which company gets promoted into which
// under-represented state, and what a promotion is allowed to disturb.
//
// The hard requirement is stability. Adding companies next month must not
// reshuffle the book, so promotion never depends on iteration order or on a
// counter — only on a hash of the cell and the domain. A newcomer either
// outranks an existing promotee for one cell, or changes nothing at all.
//
// The rest of the rules here exist because each was violated once and the
// symptom was a coverage cell that stayed short while eligible companies sat
// unused: promotions that overwrote earlier ones, promotions that robbed a
// scarce cell to fill another, and promotions onto accounts already too full
// to absorb them.

import "strings"

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
		// Bounded by the company count. A promotion is not GUARANTEED to raise
		// the count it aimed at — capContracts can drop the status it just
		// added when an account is already full — and an unbounded retry then
		// spins forever picking a new candidate that changes nothing. The
		// bound turns that into "this cell stayed short", which the verify
		// pass reports honestly.
		for range domains {
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
		if holds(p, cell) || !promotable(p, cell) || full(p, cell) {
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

// full reports whether a company already holds as much paper as it can, so a
// promotion to it would be silently dropped by capContracts and waste the
// fill loop's budget on a company that cannot absorb it.
func full(p profile, cell coverageCell) bool {
	if cell.Axis != axisContract {
		return false
	}
	// A renewal needs room for both halves of the chain.
	need := 1
	if cell.Value == "superseded" {
		need = 2
	}
	return len(p.Contracts)+need > maxContractsPerCompany
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
		p.Contracts = capContracts(p.Contracts)
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

// maxContractsPerCompany bounds how much paper one account accumulates.
//
// Coverage promotions APPEND, so a company that keeps ranking highest for
// scarce contract cells collected one of everything — six agreements on a
// single account, which no reader would believe and which spreads the
// coverage across fewer companies than the matrix intends.
const maxContractsPerCompany = 3

// capContracts keeps at most maxContractsPerCompany, dropping DUPLICATES
// first and then the newest.
//
// The order is deliberate. A renewal chain is written as a superseded
// predecessor followed by its active successor, and those two must survive
// together or the chain describes a renewal into nothing — so the front of
// the slice is what is kept.
func capContracts(statuses []string) []string {
	seen := map[string]int{}
	var out []string
	for _, status := range statuses {
		// One repeat is plausible (a company can hold two live agreements);
		// three of the same is an artefact of promotion.
		if seen[status] >= 2 {
			continue
		}
		seen[status]++
		out = append(out, status)
		if len(out) == maxContractsPerCompany {
			break
		}
	}
	return out
}

// maxInt is the larger of two ints, for bounding a hash range that a
// degenerate date span could otherwise make zero or negative.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
