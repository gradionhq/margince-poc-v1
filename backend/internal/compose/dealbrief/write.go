// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealbrief

import (
	"fmt"
	"math"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// The section kinds, spelled once; the contract's enum is the wire form.
const (
	sectionStanding = crmcontracts.DealBriefSectionKindStanding
	sectionActivity = crmcontracts.DealBriefSectionKindActivity
	sectionOpen     = crmcontracts.DealBriefSectionKindOpen
	sectionRoom     = crmcontracts.DealBriefSectionKindRoom
)

type sentence = crmcontracts.OrganizationBriefSentence

// write folds the facts into sections. A section with nothing to say is
// absent rather than empty: a heading over no sentence is furniture.
func write(f facts) []crmcontracts.DealBriefSection {
	var out []crmcontracts.DealBriefSection
	add := func(kind crmcontracts.DealBriefSectionKind, lines []sentence) {
		if len(lines) > 0 {
			out = append(out, crmcontracts.DealBriefSection{Kind: kind, Sentences: lines})
		}
	}
	add(sectionStanding, standing(f))
	add(sectionActivity, activity(f))
	add(sectionOpen, open(f))
	add(sectionRoom, room(f))
	if out == nil {
		out = []crmcontracts.DealBriefSection{}
	}
	return out
}

func cite(kind crmcontracts.OrganizationBriefEvidenceEntityType, id openapi_types.UUID, name string) []crmcontracts.OrganizationBriefEvidence {
	ev := crmcontracts.OrganizationBriefEvidence{EntityType: kind, EntityId: id}
	if name != "" {
		ev.Name = &name
	}
	return []crmcontracts.OrganizationBriefEvidence{ev}
}

func dealCite(f facts) []crmcontracts.OrganizationBriefEvidence {
	return cite(crmcontracts.OrganizationBriefEvidenceEntityTypeDeal, f.deal.Id, f.deal.Name)
}

// standing: value, close date, status, and the health reading.
func standing(f facts) []sentence {
	var lines []sentence
	d := f.deal
	switch d.Status {
	case crmcontracts.DealStatusWon:
		lines = append(lines, sentence{Text: fmt.Sprintf("%s is won%s.", d.Name, closedWhen(d)), Evidence: dealCite(f)})
	case crmcontracts.DealStatusLost:
		lines = append(lines, sentence{Text: fmt.Sprintf("%s is lost%s.", d.Name, closedWhen(d)), Evidence: dealCite(f)})
	default:
		lines = append(lines, sentence{Text: openLine(f), Evidence: dealCite(f)})
	}
	if f.health != nil && d.Status == crmcontracts.DealStatusOpen {
		lines = append(lines, sentence{Text: healthLine(f), Evidence: dealCite(f)})
	}
	return lines
}

func openLine(f facts) string {
	d := f.deal
	text := d.Name + " is open"
	if d.AmountMinor != nil && d.Currency != nil && *d.Currency != "" {
		text += fmt.Sprintf(" at %s %s", values.MajorUnits(*d.AmountMinor, *d.Currency), *d.Currency)
	}
	if d.ExpectedCloseDate != nil {
		// A date, not an instant: "past" means the calendar day has ended.
		closeDay := d.ExpectedCloseDate.Time
		if closeDay.Format("2006-01-02") < f.now.UTC().Format("2006-01-02") {
			text += fmt.Sprintf(", past its expected close of %s", closeDay.Format("2 Jan 2006"))
		} else {
			text += fmt.Sprintf(", expected to close %s", closeDay.Format("2 Jan 2006"))
		}
	}
	return text + "."
}

func closedWhen(d crmcontracts.Deal) string {
	if d.ClosedAt == nil {
		return ""
	}
	return " since " + d.ClosedAt.Format("2 Jan 2006")
}

func healthLine(f facts) string {
	h := f.health
	days := int(math.Round(h.Evidence.DaysInStage))
	text := fmt.Sprintf("Health reads %d of 100", int(math.Round(h.Health*100)))
	if h.AtRisk {
		text += " and the deal is flagged at risk"
	}
	text += fmt.Sprintf("; %s in the current stage against about %d for won deals.",
		plural(days, "day"), int(math.Round(h.Evidence.ExpectedDaysInStage)))
	return text
}

// activity: the last thing that happened, and the next booked meeting.
func activity(f facts) []sentence {
	var lines []sentence
	if last, ok := lastContact(f); ok {
		lines = append(lines, sentence{
			Text:     fmt.Sprintf("Last activity: %s, %s.", subjectOf(last), daysAgo(f.now, last.OccurredAt)),
			Evidence: cite(crmcontracts.OrganizationBriefEvidenceEntityTypeActivity, last.Id, subjectOf(last)),
		})
	}
	if next, ok := nextMeeting(f); ok {
		lines = append(lines, sentence{
			Text:     fmt.Sprintf("Next meeting: %s, %s.", subjectOf(next), next.OccurredAt.Format("2 Jan 15:04")),
			Evidence: cite(crmcontracts.OrganizationBriefEvidenceEntityTypeActivity, next.Id, subjectOf(next)),
		})
	}
	return lines
}

// open: the tasks still owed, oldest due first as the list hands them over.
func open(f facts) []sentence {
	if len(f.openTasks) == 0 {
		return nil
	}
	first := f.openTasks[0]
	count := plural(len(f.openTasks), "open task")
	if f.moreTasks {
		count = "At least " + count
	}
	text := fmt.Sprintf("%s, starting with %q", count, first.Subject)
	if first.DueAt != nil && first.DueAt.Before(f.now) {
		text += fmt.Sprintf(", overdue since %s", first.DueAt.Format("2 Jan"))
	}
	return []sentence{{
		Text:     text + ".",
		Evidence: cite(crmcontracts.OrganizationBriefEvidenceEntityTypeActivity, openapi_types.UUID(first.ID), first.Subject),
	}}
}

// room: the Deal Room's state, the buyer's open questions, their decisions.
func room(f facts) []sentence {
	if f.room == nil {
		return nil
	}
	r := f.room
	lines := []sentence{{
		Text:     fmt.Sprintf("Deal Room %q is %s%s.", r.Title, r.State, releases(r)),
		Evidence: dealCite(f),
	}}
	openBuyer := 0
	for _, th := range f.threads {
		if th.State == "open" && len(th.Comments) > 0 && th.Comments[len(th.Comments)-1].Author.Side == "buyer" {
			openBuyer++
		}
	}
	if openBuyer > 0 {
		lines = append(lines, sentence{
			Text:     fmt.Sprintf("%s from the buyer waiting for an answer.", plural(openBuyer, "thread")),
			Evidence: dealCite(f),
		})
	}
	if len(f.decisions) > 0 {
		latest := f.decisions[0]
		for _, d := range f.decisions[1:] {
			if d.CreatedAt.After(latest.CreatedAt) {
				latest = d
			}
		}
		who := "a reviewer"
		if latest.ParticipantName != nil && *latest.ParticipantName != "" {
			who = *latest.ParticipantName
		}
		lines = append(lines, sentence{
			Text:     fmt.Sprintf("Latest decision: %s %s a document, %s.", who, decisionVerb(latest.Kind), daysAgo(f.now, latest.CreatedAt)),
			Evidence: dealCite(f),
		})
	}
	return lines
}

func releases(r *crmcontracts.DealRoom) string {
	if r.ReleaseCount == nil || *r.ReleaseCount == 0 {
		return ", never published"
	}
	return fmt.Sprintf(", published %d time(s)", *r.ReleaseCount)
}

func decisionVerb(kind string) string {
	if kind == "confirm_version" {
		return "confirmed"
	}
	return "asked for changes to"
}

func lastContact(f facts) (crmcontracts.Activity, bool) {
	for _, a := range f.timeline {
		if !a.OccurredAt.After(f.now) {
			return a, true
		}
	}
	return crmcontracts.Activity{}, false
}

func nextMeeting(f facts) (crmcontracts.Activity, bool) {
	var best crmcontracts.Activity
	found := false
	for _, a := range f.timeline {
		if a.Kind != crmcontracts.ActivityKindMeeting || !a.OccurredAt.After(f.now) {
			continue
		}
		if a.MeetingStatus != nil && *a.MeetingStatus != crmcontracts.ActivityMeetingStatusBooked {
			continue
		}
		if !found || a.OccurredAt.Before(best.OccurredAt) {
			best, found = a, true
		}
	}
	return best, found
}

func subjectOf(a crmcontracts.Activity) string {
	if a.Subject != nil && *a.Subject != "" {
		return *a.Subject
	}
	return string(a.Kind)
}

// daysAgo renders an age in whole days: a brief is read at a glance, and
// "today" or "12 days ago" is what a rep says.
func daysAgo(now, at time.Time) string {
	days := int(now.Sub(at).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
