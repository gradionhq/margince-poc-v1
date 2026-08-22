// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// What one draft is written from, in A132's grounding order: the caller's
// intent, then the recipient and how we stand with them, then the deal and
// what we last committed to, then the recent conversation, then the dossier.
//
// The order is the prompt's reading order, not a preference: an instruction
// the caller typed outranks a record, a named recipient outranks the account
// in general, and what we PROMISED outranks what we merely discussed.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// Input is the account, narrowed to the one recipient and one deal this draft
// is about. It is a projection of the caller's own 360 — nothing here
// re-queries, so anything absent is absent because that caller may not see it.
type Input struct {
	// Intent is the caller's own steering ("shorter", "ask for Tuesday"). The
	// one field they typed, and the one field not fenced.
	Intent string `json:"intent,omitempty"`

	// Envelope is the correspondence this draft is written into: its language,
	// how long it has been silent, the current time and who is signing it.
	// Server-derived, never read out of the counterparty's own text.
	Envelope draftfloor.Envelope `json:"envelope"`

	Company  string `json:"company"`
	Industry string `json:"industry,omitempty"`
	// Description is the one line a person wrote about what this company does
	// (core 0203). Short, human, and the fastest way for a draft to sound like
	// it knows who it is writing to.
	Description string `json:"description,omitempty"`

	Recipient RecipientIn `json:"recipient"`
	// Deal is the opportunity the message is about, when the caller named one.
	Deal *DealIn `json:"deal,omitempty"`
	// Project is the body of work the message is about, when the caller named
	// one. The view it is folded from is already scoped to it, so Recent and
	// Commitment below describe this project's correspondence, not another's.
	Project *ProjectIn `json:"project,omitempty"`
	// Commitment is the soonest thing one side said they would do. It outranks
	// the conversation below it: a promise is a reason to write, where a
	// message is only context for one.
	Commitment *TaskIn `json:"commitment,omitempty"`
	Recent     []ActIn `json:"recent,omitempty"`
	// Dossier is what the company IS, from its own recorded facts — as opposed
	// to everything above, which is how it stands with us.
	Dossier []string `json:"dossier,omitempty"`
}

// RecipientIn is who the draft is addressed to and how we stand with them.
type RecipientIn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// FirstName is what the greeting uses. Split here rather than in the
	// prompt: a model asked to shorten a name will shorten "Dr. Anne-Marie
	// Weiß-Konrad" differently on every call.
	FirstName string `json:"first_name"`
	Title     string `json:"title,omitempty"`
	Email     string `json:"email,omitempty"`
	// Bucket is the relationship's own reading (strong/warm/weak/dormant),
	// which tells the writer how familiar to be. Never a score: a number would
	// invite the prose to quote it.
	Bucket string `json:"relationship,omitempty"`
	// LastInteraction is RFC3339 UTC, empty when we have never exchanged a
	// message with this person. Empty is the honest state and reads as "first
	// contact", not as "long ago".
	LastInteraction string `json:"last_interaction,omitempty"`
}

// DealIn is the opportunity the message is about.
type DealIn struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stage string `json:"stage,omitempty"`
	// AmountMinor never reaches the model: MarshalJSON renders `amount` from
	// it, in the currency's own scale. It carried minor units under a key that
	// said `amount_minor` to this file and nothing at all to the model, so a
	// 180,000 EUR deal read as eighteen million — the same defect the account
	// brief had (#591), and the more consequential half of it, because this
	// prompt writes an outbound message to the customer.
	AmountMinor int64  `json:"-"`
	Currency    string `json:"currency,omitempty"`
}

// MarshalJSON writes the amount as the figure a person would say — "180000.00"
// for 18000000 EUR, "18000000" for the same integer in JPY — rather than the
// minor-unit integer the column holds.
//
// Derived at the moment of writing rather than stored beside the integer, so
// the two can never disagree, and scaled by the ISO-4217 table rather than by
// /100, which understates every zero-decimal currency a hundredfold. An amount
// with no currency is not shown at all: a figure without its code is a number
// whose scale the reader has to guess, which is the defect rather than a lesser
// form of it. A zero amount IS shown — a deal deliberately priced at nothing is
// not the same as one nobody has priced.
func (d DealIn) MarshalJSON() ([]byte, error) {
	type wire DealIn // no methods, so no recursion back into this one
	amount := ""
	if d.Currency != "" {
		amount = values.MajorUnits(d.AmountMinor, d.Currency)
	}
	return json.Marshal(struct {
		wire
		Amount string `json:"amount,omitempty"`
	}{wire: wire(d), Amount: amount})
}

// ProjectIn is the body of work the message is about.
type ProjectIn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Key is the handle a human writes in a subject line, when the project
	// has one.
	Key   string `json:"key,omitempty"`
	Phase string `json:"phase"`
	// TargetEnd is the date the work is meant to finish, YYYY-MM-DD, empty
	// when nobody set one.
	TargetEnd string `json:"target_end,omitempty"`
	// OpenCommitments counts the open tasks in this project's scope — the
	// same rows Commitment's head is taken from — so the draft can say "two
	// things are still open" without naming ones it was not shown.
	OpenCommitments int `json:"open_commitments"`
}

// TaskIn is one open commitment.
type TaskIn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Due  string `json:"due,omitempty"`
}

// ActIn is one recent exchange.
type ActIn struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"`
	At      string `json:"at"`
	// Inbound says who spoke. A draft that answers what THEY said reads very
	// differently from one that follows up on what we said, and the direction
	// is the only thing that tells them apart.
	Inbound bool `json:"inbound"`
	// Snippet is the opening of what this message says.
	//
	// A subject line says a message happened; the words say what it was about.
	// Without them a draft can only gesture at a conversation it never read, and
	// on a thread whose every subject is the same string — a support thread, an
	// onboarding sequence, anything answered in place — subjects alone carry no
	// information at all, so the model fills the gap with plausible invention.
	//
	// It is what the message SAYS, not a claim about who wrote it. An activity
	// reaches an account through activity_link and the employment walk, both of
	// which record what a message concerns rather than who authored it, and the
	// 360 carries no participants — so authorship is not knowable here.
	Snippet string `json:"snippet,omitempty"`
}

// draftInputActivities bounds how much of the conversation the draft reads.
// A follow-up is about the last exchange, not the relationship's history; a
// longer window costs prefill and buys older news.
const draftInputActivities = 6

// draftInputSnippetRunes bounds how much of each message the draft reads.
//
// Enough for the opening of a real business email — a greeting, the reason for
// writing, and the ask. Past that an email is detail, which a new message asks
// about rather than repeats, and every rune of it is prompt cost on every draft.
const draftInputSnippetRunes = 400

// FromView projects the caller's 360 onto the one recipient and one deal this
// draft is about. It returns the recipient it resolved, or an error naming the
// field, so the caller's own refusal comes from one place.
func FromView(
	view crmcontracts.Organization360, req Request,
) (Input, error) {
	contact, err := findContact(view, req.PersonID)
	if err != nil {
		return Input{}, err
	}
	in := Input{
		Intent:     strings.TrimSpace(req.Intent),
		Envelope:   req.Envelope,
		Company:    view.Organization.DisplayName,
		Recipient:  recipientOf(contact),
		Recent:     foldRecent(view),
		Commitment: foldCommitment(view),
	}
	if view.Organization.Industry != nil {
		in.Industry = *view.Organization.Industry
	}
	if view.Organization.Description != nil {
		in.Description = *view.Organization.Description
	}
	// An unnamed deal is the account in general, which is ordinary — so the
	// lookup only runs when the caller named one.
	if req.DealID != "" {
		deal, dealErr := findDeal(view, req.DealID)
		if dealErr != nil {
			return Input{}, dealErr
		}
		in.Deal = &deal
	}
	if req.ProjectID != nil {
		project, projectErr := findProject(view, *req.ProjectID)
		if projectErr != nil {
			return Input{}, projectErr
		}
		in.Project = &project
	}
	return in, nil
}

// findProject folds the named project off the view's own projects section.
// The scoped read has already refused a project the caller cannot see; what
// is left to refuse here is a project the caller CAN see that is not this
// account's, which a draft about this account may not be grounded in.
func findProject(view crmcontracts.Organization360, projectID ids.ProjectID) (ProjectIn, error) {
	if view.Projects == nil {
		return ProjectIn{}, fieldError("project_id", "the account's projects are not readable by you")
	}
	for _, project := range *view.Projects {
		if ids.UUID(project.ProjectId) != projectID.UUID {
			continue
		}
		return projectFact(project, view), nil
	}
	return ProjectIn{}, fieldError("project_id", "that project is not on this account, or you cannot see it")
}

// projectFact is the one project as the draft reads it. The open-commitment
// count comes from the SCOPED next-steps section, which is this project's
// open tasks and the unfiled ones — never another project's.
func projectFact(project crmcontracts.Organization360Project, view crmcontracts.Organization360) ProjectIn {
	out := ProjectIn{
		ID:    project.ProjectId.String(),
		Name:  project.Name,
		Phase: string(project.Phase),
	}
	if project.Key != nil {
		out.Key = *project.Key
	}
	if project.TargetEndDate != nil {
		out.TargetEnd = project.TargetEndDate.Format(isoDate)
	}
	if view.NextSteps != nil {
		out.OpenCommitments = len(view.NextSteps.Data)
	}
	return out
}

// isoDate is how a date fact is written to the model: the calendar date alone,
// because a project's target end has no time of day.
const isoDate = "2006-01-02"

func recipientOf(contact crmcontracts.Organization360Contact) RecipientIn {
	out := RecipientIn{
		ID:        contact.PersonId.String(),
		Name:      contact.FullName,
		FirstName: firstName(contact.FullName),
	}
	if contact.Title != nil {
		out.Title = *contact.Title
	}
	if contact.PrimaryEmail != nil {
		out.Email = *contact.PrimaryEmail
	}
	out.Bucket = string(contact.Strength.Bucket)
	if contact.Strength.LastInteraction != nil {
		out.LastInteraction = contact.Strength.LastInteraction.UTC().Format(rfc3339)
	}
	return out
}

const rfc3339 = "2006-01-02T15:04:05Z"

// firstName is what the greeting uses. Everything before the first space, or
// the whole name when it has none — a one-word name is a name, not a mistake.
func firstName(full string) string {
	if cut, _, found := strings.Cut(strings.TrimSpace(full), " "); found && cut != "" {
		return cut
	}
	return strings.TrimSpace(full)
}

// foldCommitment takes the soonest open task. `next_steps.data` arrives
// ordered overdue → due → undated, so the head is it and this makes no
// ordering decision of its own.
func foldCommitment(view crmcontracts.Organization360) *TaskIn {
	if view.NextSteps == nil || len(view.NextSteps.Data) == 0 {
		return nil
	}
	step := view.NextSteps.Data[0]
	out := TaskIn{ID: step.ActivityId.String(), Name: step.Subject}
	if step.DueAt != nil {
		out.Due = step.DueAt.UTC().Format(rfc3339)
	}
	return &out
}

// ConversationState reads where this account's correspondence stands off the
// view's own last-message stamps.
//
// Both are absent when the caller holds no activity grant, which reads as a
// first touch. That is the conservative end of the axis and the right answer
// here: a caller who cannot see the history has no basis for a draft that
// refers to it.
func ConversationState(view crmcontracts.Organization360, now time.Time) convstate.State {
	return convstate.Classify(now, instant(view.LastInboundAt), instant(view.LastOutboundAt))
}

// instant reads one optional stamp, treating an absent one as never.
func instant(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}

// CorrespondenceText is what this account has been written to and from,
// newest first, for detecting the language its correspondence runs in.
//
// Subjects and bodies both, because a subject line rarely carries enough words
// to clear the detector's floor on its own. This drafter opens a new
// conversation, so the text is evidence about the ACCOUNT's language rather
// than about a thread being answered — a German account gets a German first
// touch even though nothing is being replied to.
func CorrespondenceText(view crmcontracts.Organization360) string {
	if view.Activities == nil {
		return ""
	}
	var text strings.Builder
	for i, act := range view.Activities.Data {
		if i == draftInputActivities {
			break
		}
		if act.Subject != nil {
			text.WriteString(*act.Subject + "\n")
		}
		if act.Body != nil {
			text.WriteString(*act.Body + "\n\n")
		}
	}
	return text.String()
}

func foldRecent(view crmcontracts.Organization360) []ActIn {
	if view.Activities == nil {
		return nil
	}
	out := make([]ActIn, 0, draftInputActivities)
	for _, act := range view.Activities.Data {
		if len(out) == draftInputActivities {
			break
		}
		item := ActIn{
			ID:      act.Id.String(),
			Kind:    string(act.Kind),
			At:      act.OccurredAt.UTC().Format(rfc3339),
			Inbound: act.Direction != nil && *act.Direction == crmcontracts.ActivityDirectionInbound,
		}
		if act.Subject != nil {
			item.Subject = *act.Subject
		}
		if act.Body != nil {
			item.Snippet = textlang.MessageOpening(*act.Body, draftInputSnippetRunes)
		}
		out = append(out, item)
	}
	return out
}

// findContact resolves the named recipient WITHIN the caller's own 360, which
// is what makes the lookup a permission check as well as a lookup: a contact
// that caller cannot see is not in the view, and the refusal is the same
// 422 as a person id that names nobody. Deliberately not a separate people
// read — that would find contacts the 360 deliberately withheld.
func findContact(
	view crmcontracts.Organization360, personID string,
) (crmcontracts.Organization360Contact, error) {
	if view.People == nil {
		return crmcontracts.Organization360Contact{}, fieldError("person_id",
			"the account's contacts are not readable by you, so there is nobody here to write to")
	}
	for _, contact := range view.People.Data {
		if contact.PersonId.String() == personID {
			return contact, nil
		}
	}
	return crmcontracts.Organization360Contact{}, fieldError("person_id",
		"that person is not a contact you can see on this account")
}

// findDeal resolves the named deal the same way. The caller checks for an
// unnamed one before calling: a draft about the account in general is an
// ordinary case rather than a missing field, so it is not this function's
// business to answer "nothing, and that is fine".
func findDeal(view crmcontracts.Organization360, dealID string) (DealIn, error) {
	if view.Deals == nil {
		return DealIn{}, fieldError("deal_id",
			"the account's deals are not readable by you")
	}
	for _, deal := range view.Deals.Data {
		if deal.DealId.String() != dealID {
			continue
		}
		out := DealIn{ID: dealID, Name: deal.Name}
		if deal.StageName != nil {
			out.Stage = *deal.StageName
		}
		if deal.Amount != nil && deal.Amount.AmountMinor != nil && deal.Amount.Currency != nil {
			out.AmountMinor = *deal.Amount.AmountMinor
			out.Currency = *deal.Amount.Currency
		}
		return out, nil
	}
	return DealIn{}, fieldError("deal_id", "that deal is not open on this account, or you cannot see it")
}

// String is the debug rendering, never the prompt payload — the prompt sends
// JSON so the model reads a structure rather than prose it might imitate.
func (in Input) String() string {
	return fmt.Sprintf("accountdraft{company:%q to:%q deal:%v}",
		in.Company, in.Recipient.Name, in.Deal != nil)
}

// Threaded is always false here, and that is what this surface IS: it opens a
// new conversation, so no subject it writes can be a reply to anything. The
// method exists so the shared check reads the same shape from every surface.
func (Input) Threaded() bool { return false }
