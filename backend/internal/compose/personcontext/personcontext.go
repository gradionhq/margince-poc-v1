// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package personcontext holds the readings of a Person360 that more than one
// prose surface takes.
//
// Three packages fold the same 360 into their own input — the person draft, the
// relationship brief, the pre-meeting brief — and three of the readings were
// byte-identical copies of each other. That is the shape that has already cost
// this program four defects: one list knows something, its copy does not, and
// the two answers diverge without anything failing.
//
// What this package is NOT is a canonical Person360 fold. The three consumers
// genuinely disagree about what they need — the draft models a deal's direction
// as a bool and takes amount and currency only as a pair, the brief allows them
// independently and carries relationship strength — and a shared shape would
// have to be the union of those disagreements, which is a worse thing than
// three honest projections. So only the readings that are the SAME move here,
// and each consumer keeps its own input struct.
package personcontext

import (
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// CurrentEmployer names where this person works now, or nothing.
//
// The 360 orders employments with the current primary first, so the first row
// is the only one that can be it. A row that is not current, or one carrying no
// organization name, answers empty rather than the next-best guess: "used to
// work at" is a different sentence from "works at", and a draft that gets it
// wrong tells somebody about a job they have left.
func CurrentEmployer(view crmcontracts.Person360) string {
	if view.Employments == nil || len(view.Employments.Data) == 0 {
		return ""
	}
	first := view.Employments.Data[0]
	if !first.IsCurrentPrimary || first.OrganizationName == nil {
		return ""
	}
	return *first.OrganizationName
}

// OmittedNames is what the caller could NOT see, as plain strings.
//
// Every prose surface carries this to the model for the same reason: a section
// missing because the reader lacks the grant is different from a section that
// is empty, and a writer told the difference stays silent about the subject
// instead of inferring around the gap.
func OmittedNames(omitted []crmcontracts.Person360SectionsOmitted) []string {
	if len(omitted) == 0 {
		return nil
	}
	out := make([]string, 0, len(omitted))
	for _, section := range omitted {
		out = append(out, string(section))
	}
	return out
}

// Stamp renders an optional time as RFC3339 UTC, or empty.
//
// Empty means the thing never happened, which is a fact the prose surfaces
// state honestly — "we have never written to them" rather than a zero date that
// reads as 1 January year one.
func Stamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
