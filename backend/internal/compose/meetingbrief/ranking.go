// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// One claim, one home. The sections used to each walk the same claim slice
// with overlapping kind filters, so one promise could appear three times in
// one brief, worded three ways, in whatever order the store returned it. Now
// the claims are ranked ONCE — sharpest first — and each section takes what it
// wants from the ranked set and marks it taken, so nothing is said twice and
// the first line of every section is the one that matters most.
//
// The rank is a small deterministic function of what the record says: is the
// claim still open, what kind is it, whose turn it is, how overdue, how old.
// "Blocks the next stage" has no field and no rule today, and a missing signal
// treated as zero would rank silently wrong, so it is not part of the score.

import (
	"sort"
	"time"
)

// rankedClaims is the single ordered set the sections draw from.
type rankedClaims struct {
	claims []ClaimIn
	taken  []bool
}

// rankClaims orders the input's claims, sharpest first. Stable on the input
// order for equal scores, so two briefs over one record read the same.
func rankClaims(in Input) *rankedClaims {
	ordered := make([]ClaimIn, len(in.Commitments))
	copy(ordered, in.Commitments)
	sort.SliceStable(ordered, func(i, j int) bool {
		return score(ordered[i], in.Now) > score(ordered[j], in.Now)
	})
	return &rankedClaims{claims: ordered, taken: make([]bool, len(ordered))}
}

// take hands the first untaken claim the predicate wants to its caller and
// marks it, so no later section can say it again.
func (r *rankedClaims) take(want func(ClaimIn) bool) (ClaimIn, bool) {
	for i, claim := range r.claims {
		if r.taken[i] || !want(claim) {
			continue
		}
		r.taken[i] = true
		return claim, true
	}
	return ClaimIn{}, false
}

// takeAll hands over every untaken claim the predicate wants, up to cap, in
// rank order, marking each.
func (r *rankedClaims) takeAll(want func(ClaimIn) bool, limit int) []ClaimIn {
	var out []ClaimIn
	for i, claim := range r.claims {
		if len(out) == limit {
			break
		}
		if r.taken[i] || !want(claim) {
			continue
		}
		r.taken[i] = true
		out = append(out, claim)
	}
	return out
}

// score reads the record, never guesses. An open claim outranks a settled one
// by a whole band; within the open band our own overdue promises lead, then
// the questions we owe an answer to, then our promises still inside their
// date, then what they objected to, then what they owe us, then what they
// told us matters. Inside a kind, a nearer due
// date wins, then the newer claim.
func score(claim ClaimIn, now time.Time) int {
	s := 0
	if claim.Status == statusOpen {
		s += 1000
	}
	switch claim.Kind {
	case kindCommitmentOurs:
		// A promise of ours still inside its date ranks below a question we
		// owe an answer to; once overdue it leads everything.
		s += 400
		if claim.DueAt != nil && claim.DueAt.Before(now) {
			s += 500
		}
	case kindOpenQuestion:
		s += 450
	case kindObjection:
		s += 400
	case kindCommitmentTheirs:
		s += 300
	case kindDecisionProcess:
		s += 250
	case kindPriority, kindSuccessCriterion:
		s += 200
	case kindDecision:
		s += 100
	}
	return s + urgency(claim, now) + freshness(claim, now)
}

// urgency: due sooner ranks higher; a week out is worth less than tomorrow.
func urgency(claim ClaimIn, now time.Time) int {
	if claim.DueAt == nil {
		return 0
	}
	days := max(int(claim.DueAt.Sub(now).Hours()/24), 0)
	if days >= 30 {
		return 0
	}
	return 30 - days
}

// freshness: newer first among equals; a month-old priority is stale news.
func freshness(claim ClaimIn, now time.Time) int {
	if claim.OccurredAt == nil {
		return 0
	}
	age := max(int(now.Sub(*claim.OccurredAt).Hours()/24), 0)
	if age >= 60 {
		return 0
	}
	return (60 - age) / 4
}

func ofKind(kinds ...string) func(ClaimIn) bool {
	return func(c ClaimIn) bool {
		for _, k := range kinds {
			if c.Kind == k {
				return true
			}
		}
		return false
	}
}

func openOfKind(kinds ...string) func(ClaimIn) bool {
	want := ofKind(kinds...)
	return func(c ClaimIn) bool { return c.Status == statusOpen && want(c) }
}
