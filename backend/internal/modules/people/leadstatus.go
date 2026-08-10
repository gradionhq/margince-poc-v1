// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "github.com/gradionhq/margince/backend/internal/shared/kernel/values"

// LeadStatus is the lead lifecycle vocabulary — the Go spelling of the
// lead_status CHECK (0009), kept in sync by the enumsync fitness gate.
// Domain logic branches on these constants, never on raw literals: a
// typo'd literal compiles and misbehaves silently, a typo'd constant
// does not exist.
type LeadStatus string

const (
	LeadStatusNew          LeadStatus = "new"
	LeadStatusWorking      LeadStatus = "working"
	LeadStatusPromoted     LeadStatus = "promoted"
	LeadStatusDisqualified LeadStatus = "disqualified"
)

// ParseLeadStatus is the seam guard: a set membership check at parse
// time, because LeadStatus("typo") still compiles.
func ParseLeadStatus(raw string) (LeadStatus, error) {
	switch s := LeadStatus(raw); s {
	case LeadStatusNew, LeadStatusWorking, LeadStatusPromoted, LeadStatusDisqualified:
		return s, nil
	}
	return "", &values.ParseError{
		Field: leadStatusColumn, Code: "invalid_lead_status",
		Message: "status is one of new, working, promoted, disqualified",
	}
}

// Open reports whether the lead is still workable — the one spelling of
// the "new or working" predicate that scoring and routing share.
func (s LeadStatus) Open() bool {
	return s == LeadStatusNew || s == LeadStatusWorking
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
// only place a lead in a workable status.
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
