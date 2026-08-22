// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

// How the caller's Person360 folds into the draft's Input: which claims and
// exchanges make the window, what a snippet keeps, and which promise is hoisted
// because it is the reason to write.

import (
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/personcontext"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// FromView folds the caller's 360 into the draft's input.
func FromView(view crmcontracts.Person360, req Request) Input {
	in := Input{
		Intent:          strings.TrimSpace(req.Intent),
		Envelope:        req.Envelope,
		Recipient:       recipientOf(view),
		SectionsOmitted: omittedNames(view.SectionsOmitted),
	}
	foldCommercial(&in, view)
	foldProject(&in, view, req.ProjectID)
	foldClaims(&in, view, req.Envelope.At())
	foldRecent(&in, view)
	foldMeeting(&in, view, req.Envelope.At())
	return in
}

// ConversationState reads where this correspondence stands off the view's own
// last-message stamps.
//
// It lives here rather than in the service because the two stamps it reads are
// already folded onto the recipient, so the classification and the fields it is
// derived from cannot drift apart. An unparseable stamp counts as absent, which
// at worst reads a correspondence as a first touch — the conservative end, and
// the one that assumes no history rather than inventing some.
func ConversationState(view crmcontracts.Person360, now time.Time) convstate.State {
	return convstate.Classify(now, instant(view.LastInboundAt), instant(view.LastOutboundAt))
}

// instant parses one optional stamp, treating anything unreadable as absent.
func instant(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}

// CorrespondenceText is the counterparty's own writing this draft answers,
// newest first, for detecting what language the correspondence is in.
//
// Subjects and bodies both, because a subject line rarely carries enough words
// to clear the detector's floor on its own.
func CorrespondenceText(view crmcontracts.Person360) string {
	if view.Activities == nil {
		return ""
	}
	var text strings.Builder
	for i, activity := range view.Activities.Data {
		if i == draftInputActivities {
			break
		}
		if activity.Subject != nil {
			text.WriteString(*activity.Subject + "\n")
		}
		if activity.Body != nil {
			text.WriteString(*activity.Body + "\n\n")
		}
	}
	return text.String()
}

func recipientOf(view crmcontracts.Person360) RecipientIn {
	person := view.Person
	out := RecipientIn{
		ID:           person.Id.String(),
		Name:         person.FullName,
		FirstName:    greetingName(person),
		Employer:     currentEmployer(view),
		LastInbound:  stamp(view.LastInboundAt),
		LastOutbound: stamp(view.LastOutboundAt),
	}
	if person.Title != nil {
		out.Title = *person.Title
	}
	if person.FirstName != nil {
		out.FirstName = *person.FirstName
	}
	out.Email = primaryEmail(person)
	return out
}

// greetingName falls back to the leading word of the display name when the
// record has no separate first name. A one-word name is a name, not a mistake.
func greetingName(person crmcontracts.Person) string {
	full := strings.TrimSpace(person.FullName)
	if cut, _, found := strings.Cut(full, " "); found && cut != "" {
		return cut
	}
	return full
}

// primaryEmail takes the address the record marks primary, and otherwise the
// first live one it carries — a contact with one unmarked address is still
// reachable, and refusing to address them would read the flag as permission
// when it only ranks. An archived address is skipped either way: it is an
// address somebody deliberately retired.
func primaryEmail(person crmcontracts.Person) string {
	if person.Emails == nil {
		return ""
	}
	first := ""
	for _, email := range *person.Emails {
		if email.ArchivedAt != nil {
			continue
		}
		if email.IsPrimary {
			return string(email.Email)
		}
		if first == "" {
			first = string(email.Email)
		}
	}
	return first
}

// currentEmployer names where this person works now. The 360 sorts the
// current-primary employment to index zero, so the first row is the answer.
func currentEmployer(view crmcontracts.Person360) string { return personcontext.CurrentEmployer(view) }

func foldCommercial(in *Input, view crmcontracts.Person360) {
	if view.Commercial == nil {
		return
	}
	if view.Commercial.Role != nil {
		in.Recipient.BuyingRole = *view.Commercial.Role
	}
	deal := view.Commercial.Deal
	if deal == nil {
		return
	}
	folded := DealIn{ID: deal.DealId.String(), Name: deal.Title}
	if deal.Stage != nil {
		folded.Stage = *deal.Stage
	}
	// Amount and currency are null together on the wire, and a figure without
	// its code has no scale, so both are taken or neither is.
	if deal.AmountMinor != nil && deal.Currency != nil {
		folded.AmountMinor = *deal.AmountMinor
		folded.Currency = *deal.Currency
	}
	if deal.CloseDate != nil {
		folded.CloseDate = deal.CloseDate.String()
	}
	in.Deal = &folded
}

// foldProject folds the named project off the view's own projects section —
// the ones this person holds a seat on or that their employer carries. A
// project outside that section is left unfolded, and the service refuses
// the request; the scoped read has already refused one the caller may not see.
func foldProject(in *Input, view crmcontracts.Person360, projectID *ids.ProjectID) {
	if projectID == nil || view.Projects == nil {
		return
	}
	for _, project := range *view.Projects {
		if ids.UUID(project.ProjectId) != projectID.UUID {
			continue
		}
		folded := ProjectIn{
			ID:    project.ProjectId.String(),
			Name:  project.Name,
			Phase: string(project.Phase),
		}
		if project.Key != nil {
			folded.Key = *project.Key
		}
		if project.TargetEndDate != nil {
			folded.TargetEnd = project.TargetEndDate.Format(isoDate)
		}
		if view.NextSteps != nil {
			folded.OpenCommitments = len(view.NextSteps.Data)
		}
		in.Project = &folded
		return
	}
}

// isoDate is how a date fact is written to the model: the calendar date alone,
// because a project's target end has no time of day.
const isoDate = "2006-01-02"

// foldClaims keeps the claims a message can honestly refer to. A dismissed
// claim is one a human said was never true, and writing an email from it would
// resurrect it in front of the customer.
func foldClaims(in *Input, view crmcontracts.Person360, now time.Time) {
	if view.Claims == nil {
		return
	}
	// An overdue promise of ours is why this message is being written, and the
	// claims arrive newest-first — so on a busy record the longest-overdue one,
	// which is the one that most needs saying, falls outside the window. It is
	// hoisted BEFORE the cap rather than ranked after it.
	claims := hoistOverdueOurs(*view.Claims, now)
	for _, claim := range claims {
		if len(in.Claims) == draftInputClaims {
			break
		}
		if claim.Status == crmcontracts.ConversationClaimStatusDismissed {
			continue
		}
		folded := ClaimIn{
			ID:       claim.Id.String(),
			Kind:     string(claim.Kind),
			Body:     claim.Body,
			SourceID: claim.SourceActivityId.String(),
		}
		if claim.DueAt != nil {
			folded.Due = claim.DueAt.UTC().Format(time.RFC3339)
			folded.Overdue = isOverdueOurs(claim, now)
		}
		in.Claims = append(in.Claims, folded)
	}
}

// snippetOf is the opening of a message, bounded and tidied.
//
// The work lives in textlang.MessageOpening, beside the header vocabulary it
// needs: this snippet and the account drafter's ask the same question of the
// same stored bodies, and a second copy here is how the answers would drift.
func snippetOf(body string) string {
	return textlang.MessageOpening(body, draftInputSnippetRunes)
}

// isOverdueOurs reports whether this claim is a promise WE made, still open,
// and past its date.
//
// All three conditions, because each alone is a different sentence. A
// commitment of THEIRS past its date is a fact about them and a different
// message from one we owe; a DONE commitment past its date was kept, and
// resurrecting it in front of the customer is worse than not mentioning it at
// all; and a promise still within its date is not a reason to write today.
//
// A due date exactly equal to now is not yet overdue. The boundary favours the
// side that says less.
func isOverdueOurs(claim crmcontracts.ConversationClaim, now time.Time) bool {
	if claim.Kind != crmcontracts.CommitmentOurs {
		return false
	}
	if claim.Status != crmcontracts.ConversationClaimStatusOpen {
		return false
	}
	return claim.DueAt != nil && claim.DueAt.Before(now)
}

// hoistOverdueOurs moves our overdue promises to the front, keeping every other
// claim in the order the store returned them.
//
// Stable, so the newest-first ordering the rest of the ranking depends on
// survives underneath. Only the hoist is a reordering; nothing is dropped, and
// a record with no overdue promise of ours comes back exactly as it went in.
func hoistOverdueOurs(claims []crmcontracts.ConversationClaim, now time.Time) []crmcontracts.ConversationClaim {
	out := make([]crmcontracts.ConversationClaim, 0, len(claims))
	for _, claim := range claims {
		if isOverdueOurs(claim, now) {
			out = append(out, claim)
		}
	}
	if len(out) == 0 {
		return claims
	}
	for _, claim := range claims {
		if !isOverdueOurs(claim, now) {
			out = append(out, claim)
		}
	}
	return out
}

func foldRecent(in *Input, view crmcontracts.Person360) {
	if view.Activities == nil {
		return
	}
	readInbound := false
	for _, activity := range view.Activities.Data {
		if len(in.Recent) == draftInputActivities {
			break
		}
		folded := ActIn{
			ID:      activity.Id.String(),
			Kind:    string(activity.Kind),
			At:      activity.OccurredAt.UTC().Format(time.RFC3339),
			Inbound: activity.Direction != nil && *activity.Direction == crmcontracts.ActivityDirectionInbound,
		}
		if activity.Subject != nil {
			folded.Subject = *activity.Subject
		}
		// The newest INBOUND message, and only that one. Our own outbound is
		// text this side already wrote, and a second inbound invites the draft
		// to answer two conversations at once.
		//
		// The flag is set on the newest inbound whether or not it yielded text,
		// so an empty body reads as "nothing to quote" rather than falling
		// through to an OLDER message the prompt would then present as the
		// current one.
		if folded.Inbound && !readInbound {
			readInbound = true
			if activity.Body != nil {
				folded.Snippet = snippetOf(*activity.Body)
			}
		}
		in.Recent = append(in.Recent, folded)
	}
}

// omittedNames renders the withheld sections as plain strings for the writer.
// The contract types them as an enum; the draft only needs the names.
func omittedNames(omitted []crmcontracts.Person360SectionsOmitted) []string {
	return personcontext.OmittedNames(omitted)
}

// stamp renders an optional instant in one fixed format, so two timestamps
// compare as strings the way the instants they name compare.
func stamp(at *time.Time) string { return personcontext.Stamp(at) }

// Threaded reports whether a real inbound message earns this draft a reply
// prefix. Only a message THEY sent counts: our own last outbound carries a
// subject too, and "Re:" on it replies to ourselves.
func (in Input) Threaded() bool {
	return len(in.Recent) > 0 && in.Recent[0].Inbound && in.Recent[0].Subject != ""
}

// foldMeeting carries the next meeting this person is actually on.
//
// Two conditions, and both are refusals rather than filters. A meeting already
// past is not a next meeting - the section can hold a stale row, and a draft
// referring to it as upcoming is wrong in the way a reader notices. And a
// meeting whose participant list does not include this person is somebody
// else's: naming it to them discloses a meeting they were not invited to,
// which is the privacy line this grounding sits closest to.
func foldMeeting(in *Input, view crmcontracts.Person360, now time.Time) {
	meeting := view.NextMeeting
	if meeting == nil || !meeting.StartsAt.After(now) {
		return
	}
	if !attends(meeting, view.Person.Id) {
		return
	}
	folded := MeetingIn{StartsAt: meeting.StartsAt.UTC().Format(time.RFC3339)}
	if meeting.Subject != nil {
		folded.Subject = *meeting.Subject
	}
	in.Meeting = &folded
}

// attends reports whether this person is on the meeting.
//
// An absent participant list answers NO. The list is the evidence, and a
// meeting that carries none has not shown that they are on it - assuming they
// are is exactly the disclosure this guard exists to prevent.
func attends(meeting *crmcontracts.Person360NextMeeting, personID openapi_types.UUID) bool {
	if meeting.Participants == nil {
		return false
	}
	for _, participant := range *meeting.Participants {
		if participant.PersonId == personID {
			return true
		}
	}
	return false
}
