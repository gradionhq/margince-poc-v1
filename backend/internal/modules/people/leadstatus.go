// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "github.com/gradionhq/margince/backend/internal/shared/kernel/values"

// LeadStatus is the lead lifecycle vocabulary — the Go spelling of the
// lead_status CHECK, kept in sync by the enumsync fitness gate. Domain
// logic branches on these constants, never on raw literals: a typo'd
// literal compiles and misbehaves silently, a typo'd constant does not
// exist.
//
// The open states are a ladder the system climbs from captured activity
// (new → contacted when we reach out → engaged when they answer or a
// meeting is booked or held) and a human may place by hand; the terminal
// pair is reached only through the governed promote and disqualify paths.
type LeadStatus string

const (
	LeadStatusNew          LeadStatus = "new"
	LeadStatusContacted    LeadStatus = "contacted"
	LeadStatusEngaged      LeadStatus = "engaged"
	LeadStatusPromoted     LeadStatus = "promoted"
	LeadStatusDisqualified LeadStatus = "disqualified"
)

// openLeadStatuses is the open set in ladder order, for the Go that ranks
// it. The SQL predicates that select workable leads spell the same list as
// a literal, module by module, because a fragment cannot cross the module
// boundary — TestLadderClimbsFromActivityAndNeverDescends holds the ladder.
var openLeadStatuses = []LeadStatus{LeadStatusNew, LeadStatusContacted, LeadStatusEngaged}

// ParseLeadStatus is the seam guard: a set membership check at parse
// time, because LeadStatus("typo") still compiles.
func ParseLeadStatus(raw string) (LeadStatus, error) {
	switch s := LeadStatus(raw); s {
	case LeadStatusNew, LeadStatusContacted, LeadStatusEngaged, LeadStatusPromoted, LeadStatusDisqualified:
		return s, nil
	}
	return "", &values.ParseError{
		Field: leadStatusColumn, Code: "invalid_lead_status",
		Message: "status is one of new, contacted, engaged, promoted, disqualified",
	}
}

// Open reports whether the lead is still workable — the one spelling of
// the "on the ladder" predicate that scoring and routing share.
func (s LeadStatus) Open() bool {
	return s.rung() >= 0
}

// rung is the status's position on the open ladder, -1 off it.
func (s LeadStatus) rung() int {
	for i, open := range openLeadStatuses {
		if open == s {
			return i
		}
	}
	return -1
}

// Advances reports whether moving from s to target is a step UP the open
// ladder — the only move the system makes on its own. A human may also step
// down; a terminal lead is never moved by either.
func (s LeadStatus) Advances(target LeadStatus) bool {
	from, to := s.rung(), target.rung()
	return from >= 0 && to > from
}

// parseWritableLeadStatus is the create/update seam guard: it parses a
// status AND refuses the terminal states. promoted and disqualified are
// reached ONLY through their governed lifecycle handlers — promote (which
// requires person:create) mints the person, carries the consent and emits
// lead.promoted; disqualify (which requires lead:delete) archives the row.
// A bare lead:update PATCH setting status='promoted' directly would skip
// all of that: it strands the lead in a dead state no later promote can
// recover, breaks the disqualified⇒archived invariant, and evades the
// distinct scope the governed paths require. The ordinary write path may
// only place a lead on the open ladder.
func parseWritableLeadStatus(raw string) (LeadStatus, error) {
	s, err := ParseLeadStatus(raw)
	if err != nil {
		return "", err
	}
	if !s.Open() {
		return "", &values.ParseError{
			Field: "status", Code: "terminal_lead_status",
			Message: "promoted and disqualified are reached through the promote and disqualify actions, not by editing status",
		}
	}
	return s, nil
}
