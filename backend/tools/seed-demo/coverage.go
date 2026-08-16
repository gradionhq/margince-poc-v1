// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What a demo installation has to contain, expressed as a matrix rather than
// as a list of companies.
//
// Two goals pull in opposite directions here. A DEMO wants realistic
// proportions: most companies are targets nobody has called yet, a few are
// customers, one or two churned. TEST data wants the opposite — every state
// the product can hold must exist at least once, including the rare ones
// (a cancelled contract, a lost deal for each reason, a disqualified lead),
// because a screen with no row is a screen nobody can check.
//
// Coverage wins first, then proportions fill the rest. The matrix below is
// the floor: each cell names a state and how many companies must reach it.
// Everything past the floor is assigned at realistic weights.
//
// The alternative — hand-listing which company gets what — is what demo.json
// does for the five story customers, and it does not scale past them. At 200
// companies a list is a thing somebody forgets to update, and the failure is
// silent: the demo simply stops covering a state and nobody notices until a
// screen is empty during a customer call.

import (
	"fmt"
	"sort"
)

// coverageAxis is one dimension of the matrix. Every company gets exactly one
// value on each axis, so the axes are independent and a company can be
// promoted on one without disturbing another.
type coverageAxis string

const (
	axisLifecycle coverageAxis = "lifecycle"
	axisDeal      coverageAxis = "deal"
	axisContract  coverageAxis = "contract"
	axisLead      coverageAxis = "lead"
	axisProject   coverageAxis = "project"
	axisDocument  coverageAxis = "document"
)

// coverageCell is one state that must exist, and how many companies must be
// in it before the demo is considered complete.
//
// minCount is above 1 wherever a single example would be misleading: one won
// deal looks like an accident on a board, and a reviewer cannot tell a
// deliberate state from a stray row. Three is enough to read as a pattern.
type coverageCell struct {
	Axis  coverageAxis
	Value string
	Min   int
	// Why the cell exists, printed when the verify pass finds it short. A
	// reader who has never seen this file should learn what broke from the
	// failure alone.
	Because string
}

// coverageMatrix is the floor every seeded installation must clear.
//
// Ordered deliberately: the scarcest states come first, so when companies are
// short the rarest cells still get filled. A cell added here starts failing
// verify immediately, which is the point — the matrix is the specification.
var coverageMatrix = []coverageCell{
	// Lifecycle. The filter whose whole job is "who are our customers?"
	// returns everything when every account sits at one value.
	{axisLifecycle, "customer", 8, "the revenue card, contracts and invoices all hang off being a customer"},
	{axisLifecycle, "former_customer", 3, "churn is a state the product must show, and an expired contract is what says so"},
	{axisLifecycle, "opportunity", 8, "an account with an open deal"},
	{axisLifecycle, "prospect", 10, "contacted, no deal yet"},
	{axisLifecycle, "target", 20, "the untouched majority — a demo where everything is worked looks invented"},

	// Deals. Every stage has to hold cards or the board reads as broken, and
	// both closed states must exist with their reasons.
	{axisDeal, "qualified", 4, "the first stage a deal reaches"},
	{axisDeal, "discovery", 4, "mid-funnel, where most live"},
	{axisDeal, "proposal", 4, "an offer is out — the stage that pairs with an offer record"},
	{axisDeal, "negotiation", 3, "late stage, where contracts get drafted"},
	{axisDeal, "won", 6, "a won deal is what makes a customer, and what a contract points at"},
	{axisDeal, "lost", 4, "each lost_reason must appear, or the reason filter has nothing to filter"},

	// Contracts. The states are only reachable through real transitions, so
	// each one proves a different path works.
	{axisContract, "draft", 3, "unsigned paper"},
	{axisContract, "active", 8, "the live agreement behind a customer"},
	{axisContract, "expired", 3, "what a former customer is left holding"},
	{axisContract, "cancelled", 2, "reached only by recording a cancellation, never by asserting the status"},
	{axisContract, "superseded", 2, "reached only through /renewal, which writes the successor in the same transaction"},

	// Leads. The funnel above the pipeline.
	{axisLead, "new", 4, "untouched inbound"},
	{axisLead, "working", 4, "an SDR has it"},
	{axisLead, "promoted", 3, "became a person and a company — the path that proves promotion"},
	{axisLead, "disqualified", 2, "archived, and only visible with include_archived — a state easy to seed wrong"},

	// Documents. Paper is only useful when it is attached to the right thing.
	{axisDocument, "contract_pdf", 8, "paper attached to its contract, not floating in Documents"},
	{axisDocument, "loose", 5, "an NDA or a price list belongs to the account, not to a contract"},
}

// plannedOnlyMatrix is states the PLANNER assigns but no phase writes yet.
//
// Keeping them out of coverageMatrix is deliberate: verify reads the
// installation, so a cell here would fail every run for work that is simply
// not built, and a gate that always fails is a gate people learn to ignore.
// They stay listed so the planner keeps reserving companies for them and the
// day the phase lands the cells move up with no re-planning.
var plannedOnlyMatrix = []coverageCell{
	{axisProject, "initiative", 2, "a project before it is pursued — no project phase in the seeder yet"},
	{axisProject, "delivering", 3, "the phase a customer's project sits in — no project phase in the seeder yet"},
	{axisProject, "closed", 2, "finished work, which needs a closing reason — no project phase in the seeder yet"},
}

// planningMatrix is what the PLANNER satisfies: what verify checks, plus what
// is planned ahead of its phase.
func planningMatrix() []coverageCell {
	return append(append([]coverageCell(nil), coverageMatrix...), plannedOnlyMatrix...)
}

// orderedCells is the matrix in the order the planner must satisfy it.
//
// Lifecycle first, and this is load-bearing rather than tidy: promoting a
// company's lifecycle rewrites its contracts and its project, because those
// have to agree (a customer owns paper, a target does not). Filling contracts
// before lifecycle therefore threw the contract work away on the next
// promotion, and the cell stayed short while eligible candidates went unused.
//
// Deals come next for the same reason — a deal's stage constrains which
// contracts are believable — then everything that depends on both.
func orderedCells() []coverageCell {
	rank := map[coverageAxis]int{
		axisLifecycle: 0,
		axisDeal:      1,
		axisContract:  2,
		axisProject:   3,
		axisLead:      4,
		axisDocument:  5,
	}
	out := planningMatrix()
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Axis] < rank[out[j].Axis] })
	return out
}

// coverageTarget is a matrix as a lookup: axis -> value -> minimum.
func coverageTarget(cells []coverageCell) map[coverageAxis]map[string]int {
	out := map[coverageAxis]map[string]int{}
	for _, cell := range cells {
		if out[cell.Axis] == nil {
			out[cell.Axis] = map[string]int{}
		}
		out[cell.Axis][cell.Value] = cell.Min
	}
	return out
}

// coverageShortfall reports which cells a set of assignments does not reach.
//
// It is the same function the planner uses to decide what to promote and the
// verify pass uses to decide whether to fail, so the two can never disagree
// about what "covered" means.
func coverageShortfall(cells []coverageCell, counts map[coverageAxis]map[string]int) []string {
	var short []string
	for _, cell := range cells {
		got := counts[cell.Axis][cell.Value]
		if got >= cell.Min {
			continue
		}
		short = append(short, fmt.Sprintf(
			"%s=%s: %d of %d — %s", cell.Axis, cell.Value, got, cell.Min, cell.Because))
	}
	sort.Strings(short)
	return short
}

// minCompaniesForCoverage is how many companies the matrix needs before it can
// possibly be satisfied, counting only the axis that demands the most.
//
// Axes are independent — one company can be a customer AND hold a lost deal —
// so the requirement is the largest single axis, not the sum. Reported by the
// planner when the dataset is too small, because "coverage failed" on a
// 12-company dataset is a fact about the dataset and not a bug.
func minCompaniesForCoverage() int {
	perAxis := map[coverageAxis]int{}
	for _, cell := range planningMatrix() {
		perAxis[cell.Axis] += cell.Min
	}
	worst := 0
	for _, total := range perAxis {
		if total > worst {
			worst = total
		}
	}
	return worst
}
