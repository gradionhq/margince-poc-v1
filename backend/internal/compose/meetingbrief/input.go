// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// What one brief is written from.
//
// Nothing here re-queries. Every field is folded out of the meeting read and
// the composite 360 the caller already made, which is what makes the brief's
// scope exactly the reader's own scope without a second set of gates to keep in
// agreement.

import (
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Input is the room, the deal, what was promised, and what recently happened.
type Input struct {
	ActivityID string
	Subject    string
	StartsAt   time.Time
	// Now is the instant the brief was assembled, carried so "days since last
	// touch" is arithmetic on one clock rather than on time.Now() called from
	// several places that could straddle a midnight.
	Now time.Time

	Company   string
	Deal      *DealIn
	Attendees []AttendeeIn
	// Commitments are the room's open promises and questions, ours and theirs,
	// flattened across attendees because the reader asks "what is outstanding
	// with these people", not "what is outstanding with each of them".
	Commitments []ClaimIn
	// Recent is the newest captured conversation with the lead attendee first.
	Recent []ActIn
	// LastTouchAt is the newest conversation with anyone in the room before
	// this meeting. Nil means nothing was ever captured with any of them.
	LastTouchAt *time.Time
}

// DealIn is the deal this meeting is about.
type DealIn struct {
	ID          string
	Name        string
	Stage       string
	AmountMinor int64
	Currency    string
	CloseDate   *time.Time
}

// AttendeeIn is one person in the room.
type AttendeeIn struct {
	PersonID  string
	FullName  string
	Title     string
	DealRole  string
	LastTouch *time.Time
	FirstTime bool
}

// ClaimIn is one thing somebody in the room said. The kind rides along because
// "they promised to send it" and "we promised to send it" are opposite
// obligations, and the body alone loses which one it was.
type ClaimIn struct {
	PersonName string
	Kind       string
	Body       string
	Status     string
	SourceID   string
	// SourceLabel names the conversation in prose, so a sentence can say where
	// the promise was made without pasting a record id into the text.
	SourceLabel string
	DueAt       *time.Time
}

// ActIn is one recent conversation as the brief reads it.
type ActIn struct {
	ID        string
	Kind      string
	Subject   string
	Direction string
	At        time.Time
}

// FromMeeting folds the gated meeting read into the brief's input.
func FromMeeting(room meeting, perAttendee map[ids.UUID][]crmcontracts.ConversationClaim, now time.Time) Input {
	in := Input{
		ActivityID: room.ID.String(),
		Subject:    room.Subject,
		StartsAt:   room.StartsAt.UTC(),
		Now:        now,
	}
	if room.Deal != nil {
		in.Deal = &DealIn{
			ID:          room.Deal.ID.String(),
			Name:        room.Deal.Name,
			Stage:       room.Deal.Stage,
			Currency:    room.Deal.Currency,
			CloseDate:   room.Deal.CloseDate,
			AmountMinor: derefInt(room.Deal.AmountMinor),
		}
	}
	for _, attendee := range room.Attendees {
		in.Attendees = append(in.Attendees, AttendeeIn{
			PersonID:  attendee.PersonID.String(),
			FullName:  attendee.FullName,
			Title:     attendee.Title,
			DealRole:  attendee.DealRole,
			LastTouch: attendee.LastTouch,
			FirstTime: attendee.firstTime(),
		})
		in.LastTouchAt = latest(in.LastTouchAt, attendee.LastTouch)
		in.Commitments = append(in.Commitments, foldClaims(attendee.FullName, perAttendee[attendee.PersonID])...)
	}
	return in
}

// foldClaims turns one attendee's claims into the brief's shape, dropping the
// dismissed ones.
//
// A dismissed claim is one a human said was never true. Writing prep from it
// would resurrect it in front of the person it was wrong about, which is the
// worst place for the correction to have been ignored.
func foldClaims(personName string, found []crmcontracts.ConversationClaim) []ClaimIn {
	out := make([]ClaimIn, 0, len(found))
	for _, claim := range found {
		if claim.Status == crmcontracts.ConversationClaimStatusDismissed {
			continue
		}
		folded := ClaimIn{
			PersonName: personName,
			Kind:       string(claim.Kind),
			Body:       claim.Body,
			Status:     string(claim.Status),
			SourceID:   ids.UUID(claim.SourceActivityId).String(),
			DueAt:      claim.DueAt,
		}
		if claim.SourceLabel != nil {
			folded.SourceLabel = *claim.SourceLabel
		}
		out = append(out, folded)
	}
	return out
}

// WithCounterpart folds in what the lead attendee's own page already knows:
// where they work, and what was last said.
//
// The 360 is the read the person page itself serves, so anything the brief says
// from it is something the reader could have seen by opening that page. It
// never OVERRIDES the meeting read — the deal on the invite is the deal this
// meeting is about, and the person's leading open deal may be a different one.
func WithCounterpart(in *Input, view crmcontracts.Person360) {
	in.Company = currentEmployer(view)
	if in.Deal == nil {
		in.Deal = dealFromView(view)
	}
	if view.Activities == nil {
		return
	}
	for _, activity := range view.Activities.Data {
		if len(in.Recent) == recentCap {
			break
		}
		folded := ActIn{
			ID:   ids.UUID(activity.Id).String(),
			Kind: string(activity.Kind),
			At:   activity.OccurredAt.UTC(),
		}
		if activity.Subject != nil {
			folded.Subject = *activity.Subject
		}
		if activity.Direction != nil {
			folded.Direction = string(*activity.Direction)
		}
		in.Recent = append(in.Recent, folded)
	}
}

// recentCap bounds the timeline the deal-state section reads. The brief is a
// two-to-three-minute read; a longer window buys nothing a reader will see.
const recentCap = 10

// dealFromView takes the person's leading open deal when the invite named no
// deal at all. A meeting linked to no deal is common — the calendar event was
// captured before anyone filed it — and refusing to say what is commercially at
// stake because of that would leave the reader worse informed than the person
// page they came from.
func dealFromView(view crmcontracts.Person360) *DealIn {
	if view.Commercial == nil || view.Commercial.Deal == nil {
		return nil
	}
	deal := view.Commercial.Deal
	out := &DealIn{ID: ids.UUID(deal.DealId).String(), Name: deal.Title}
	if deal.Stage != nil {
		out.Stage = *deal.Stage
	}
	if deal.AmountMinor != nil {
		out.AmountMinor = *deal.AmountMinor
	}
	if deal.Currency != nil {
		out.Currency = *deal.Currency
	}
	if deal.CloseDate != nil {
		closeDate := deal.CloseDate.Time
		out.CloseDate = &closeDate
	}
	return out
}

// currentEmployer names where the counterpart works now. The 360 sorts the
// current-primary employment to index zero, so the first row is the answer.
func currentEmployer(view crmcontracts.Person360) string {
	if view.Employments == nil || len(view.Employments.Data) == 0 {
		return ""
	}
	first := view.Employments.Data[0]
	if !first.IsCurrentPrimary || first.OrganizationName == nil {
		return ""
	}
	return *first.OrganizationName
}

// latest keeps the newer of two optional instants, treating nil as "nothing
// captured" rather than as the zero time — the zero time would win every
// comparison and report a relationship as touched in year one.
func latest(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.After(*current) {
		return candidate
	}
	return current
}

func derefInt(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
