// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package network answers the relationship questions that span modules:
// who on our team knows this account, and which deals are at risk because of
// how — or by whom — they are covered.
//
// It lives in compose because every answer joins deals, people, activities and
// the interaction projection, and a module never imports a sibling.
//
// THE THRESHOLDS ARE NOT INVENTED HERE. Single-threading, the no-touch windows
// and the won-but-silent window are REPORT-PARAM-1..3 in the normative
// reporting chapter, and the engaged-stakeholder test is the one the deal
// health engine already uses. A second spelling of any of them would let two
// screens disagree about whether the same deal is at risk, which is exactly
// what reporting.md forbids: a flag must reconcile across every surface that
// shows it.
package network

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// Risk kinds. Each is a NAMED rule with a normative source, so a flag on a
// screen can be traced to the sentence that defines it.
const (
	// RiskSingleThreadedTheirs is REPORT-PARAM-1 verbatim: fewer than two
	// engaged contacts on an open deal. Their side — the customer is
	// represented by one person, and if that person leaves or goes quiet the
	// deal has no other way in.
	RiskSingleThreadedTheirs = "single_threaded_theirs"
	// RiskSingleThreadedOurs is GRAPH-RISK-1, and it is genuinely NEW rather
	// than a re-reading of the reporting rule — which is why it carries its
	// own id instead of being smuggled in beside it. It is a statement about
	// OUR coverage: one colleague carries almost all the contact, so the deal
	// depends on their availability, their memory, and their staying.
	RiskSingleThreadedOurs = "single_threaded_ours"
	// RiskChampionLeft fires only for the canonical champion seat. Another
	// role leaving is worth saying and is not the same event.
	RiskChampionLeft    = "champion_left"
	RiskStakeholderLeft = "stakeholder_left"
	// RiskGoingCold is REPORT-PARAM-2: no captured touch for 30 or 60 days.
	RiskGoingCold = "going_cold"
	// RiskCoverageGap is a deal with seats but no engaged champion.
	RiskCoverageGap = "coverage_gap"
)

// ourSideDominanceShare and ourSideMinInteractions are GRAPH-RISK-1's
// constants. Both are needed: a share alone would flag a deal where one
// colleague sent the only two messages there have ever been, which is a young
// deal rather than a concentrated one.
const (
	ourSideDominanceShare  = 0.8
	ourSideMinInteractions = 5
)

// Risk is one finding, carrying the evidence that produced it. A risk without
// evidence is an opinion, and the surfaces that render these are required to
// let a human drill into why (REPORT-AC-3).
type Risk struct {
	Kind    string
	DealID  ids.UUID
	Summary string
	// PersonIDs and UserIDs are the records the finding is ABOUT — the
	// unengaged stakeholder, the colleague carrying the thread. They are ids
	// rather than names so the caller renders them under its own row scope.
	PersonIDs []ids.UUID
	UserIDs   []ids.UUID
	// DaysSinceTouch is set on going-cold; zero elsewhere.
	DaysSinceTouch int
}

// DealCoverage is the whole picture for one deal: who sits on it, who is
// actually engaged, which of our people carry it, and what is wrong.
type DealCoverage struct {
	DealID       ids.UUID
	Stakeholders []deals.DealStakeholder
	// OurSide is the colleagues with recorded interaction with the deal's
	// stakeholders, warmest first.
	OurSide []ColleagueEdge
	Risks   []Risk
}

// ColleagueEdge is one of our people's relationship with one contact, scored.
type ColleagueEdge struct {
	UserID   ids.UUID
	PersonID ids.UUID
	Strength relstrength.Score
	Count90d int
}

// CoverageFor assembles one deal's coverage and its risks.
//
// Read inside ONE transaction at ONE instant: a coverage view whose stakeholder
// list and engagement test came from different snapshots can report a deal as
// single-threaded while listing three engaged contacts.
func CoverageFor(ctx context.Context, tx pgx.Tx, dealID ids.DealID, now time.Time) (DealCoverage, error) {
	out := DealCoverage{DealID: dealID.UUID}

	stakeholders, err := deals.Stakeholders(ctx, tx, dealID, now)
	if err != nil {
		return out, err
	}
	out.Stakeholders = stakeholders

	people := make([]ids.UUID, 0, len(stakeholders))
	for _, s := range stakeholders {
		people = append(people, s.PersonID)
	}
	edges, err := search.EdgesForPeople(ctx, tx, people)
	if err != nil {
		return out, err
	}
	for _, e := range edges {
		out.OurSide = append(out.OurSide, ColleagueEdge{
			UserID: e.UserID, PersonID: e.PersonID,
			Strength: e.StrengthOf(now), Count90d: e.Count90d,
		})
	}

	out.Risks = foldRisks(out)
	return out, nil
}

// foldRisks is the pure half: given the gathered facts, decide what is wrong.
// Pure so it can be tested against hand-built inputs with no database — the
// gather/fold split every detector in this codebase uses.
//
// It takes no clock because none of the rules here needs one: engagement and
// recency were already resolved during the gather, against a single instant.
// The going-cold detector will need one when it lands, and it can take it
// then — carrying an unused parameter now would only advertise a capability
// this fold does not have.
func foldRisks(c DealCoverage) []Risk {
	var risks []Risk

	// REPORT-PARAM-1, verbatim: distinct_engaged_contacts < 2.
	engaged := make([]ids.UUID, 0, len(c.Stakeholders))
	for _, s := range c.Stakeholders {
		if s.Engaged {
			engaged = append(engaged, s.PersonID)
		}
	}
	if len(engaged) < reportThreadingFloor {
		risks = append(risks, Risk{
			Kind: RiskSingleThreadedTheirs, DealID: c.DealID, PersonIDs: engaged,
			Summary: "fewer than two engaged contacts — the deal rests on one relationship",
		})
	}

	// GRAPH-RISK-1: one of OUR people carries almost all the contact.
	if r, found := ourSideConcentration(c); found {
		risks = append(risks, r)
	}

	// A deal with seats but no engaged champion. Distinct from
	// single-threading: three engaged contacts and no champion among them is
	// a deal nobody inside is arguing for.
	if !hasEngagedChampion(c.Stakeholders) && len(c.Stakeholders) > 0 {
		risks = append(risks, Risk{
			Kind: RiskCoverageGap, DealID: c.DealID,
			Summary: "no engaged champion — nobody inside the account is carrying this",
		})
	}
	return risks
}

// reportThreadingFloor is REPORT-PARAM-1's value, named rather than inline so
// the constant a support conversation quotes is the constant compared against.
const reportThreadingFloor = 2

// ourSideConcentration is GRAPH-RISK-1: one colleague holding at least
// ourSideDominanceShare of at least ourSideMinInteractions interactions.
//
// The minimum matters as much as the share. Without it a deal where one person
// sent the only two messages that have ever been exchanged would flag as
// concentrated, when it is simply new.
func ourSideConcentration(c DealCoverage) (Risk, bool) {
	total := 0
	byUser := map[ids.UUID]int{}
	for _, e := range c.OurSide {
		total += e.Count90d
		byUser[e.UserID] += e.Count90d
	}
	if total < ourSideMinInteractions {
		return Risk{}, false
	}
	for user, n := range byUser {
		if float64(n) >= ourSideDominanceShare*float64(total) {
			return Risk{
				Kind: RiskSingleThreadedOurs, DealID: c.DealID, UserIDs: []ids.UUID{user},
				Summary: "one colleague carries almost all the contact — the deal depends on their availability",
			}, true
		}
	}
	return Risk{}, false
}

// hasEngagedChampion answers whether any engaged seat is the champion.
func hasEngagedChampion(stakeholders []deals.DealStakeholder) bool {
	for _, s := range stakeholders {
		if s.Engaged && s.Role == roleChampion {
			return true
		}
	}
	return false
}

// roleChampion is the canonical champion seat — the role champion-left fires
// on, and the one a coverage gap looks for.
const roleChampion = "champion"
