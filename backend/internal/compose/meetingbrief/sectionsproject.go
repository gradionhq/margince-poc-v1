// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// How the brief speaks about the body of work a meeting belongs to.
//
// A delivery meeting is the case these lines exist for: months after
// close-won there is often no open deal at all, and the sections that lead the
// brief fell silent exactly when the engagement was the whole point.

import (
	"fmt"
	"strings"
	"time"
)

// projectHeaderLine names the engagement and where it stands. The key rides
// along when the project has one, because that is the string a reader sees in
// subject lines all day and recognises faster than the name.
func projectHeaderLine(project ProjectIn) string {
	parts := []string{project.Name}
	if project.Key != "" {
		parts[0] = fmt.Sprintf("%s (%s)", project.Name, project.Key)
	}
	if project.Phase != "" {
		parts = append(parts, project.Phase)
	}
	if project.TargetEndDate != nil {
		parts = append(parts, "target "+project.TargetEndDate.Format("2 Jan 2006"))
	}
	return strings.Join(parts, " · ") + "."
}

func meetingLine(in Input) string {
	subject := in.Subject
	if subject == "" {
		subject = "Meeting"
	}
	when := in.StartsAt.Format("Mon 2 Jan 15:04 MST")
	if in.Company == "" {
		return fmt.Sprintf("%s, %s.", subject, when)
	}
	return fmt.Sprintf("%s with %s, %s.", subject, in.Company, when)
}

// projectGoalLine states where the engagement stands and what it is running
// at. The target date is named only when the record carries one — a deadline
// nobody set is the invented context the grounding rule forbids.
func projectGoalLine(project ProjectIn) string {
	if project.TargetEndDate == nil {
		return fmt.Sprintf("Move %s on from %s.", project.Name, phaseOrUnnamed(project.Phase))
	}
	return fmt.Sprintf("Move %s on from %s, against a target of %s.",
		project.Name, phaseOrUnnamed(project.Phase),
		project.TargetEndDate.Format("2 Jan 2006"))
}

func phaseOrUnnamed(phase string) string {
	if phase == "" {
		return "its current phase"
	}
	return phase
}

// priorMeetingLine says when this room last met and what it was called.
//
// Days ago, not a date, for the same reason the last-touch line is: the reader
// is placing it against today, and a date makes them do the arithmetic. The
// subject rides along because "three weeks ago" alone does not tell them which
// conversation they are being reminded of.
func priorMeetingLine(prior PriorMeetingIn, now time.Time) string {
	subject := prior.Subject
	if subject == "" {
		subject = "A meeting"
	}
	days := int(now.Sub(prior.StartsAt).Hours() / 24)
	switch {
	case days <= 0:
		return fmt.Sprintf("You met earlier today: %s.", subject)
	case days == 1:
		return fmt.Sprintf("You met yesterday: %s.", subject)
	default:
		return fmt.Sprintf("You met %d days ago: %s.", days, subject)
	}
}
