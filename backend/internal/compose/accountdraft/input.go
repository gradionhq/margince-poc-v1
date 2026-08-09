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
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// Input is the account, narrowed to the one recipient and one deal this draft
// is about. It is a projection of the caller's own 360 — nothing here
// re-queries, so anything absent is absent because that caller may not see it.
type Input struct {
	// Intent is the caller's own steering ("shorter", "ask for Tuesday"). The
	// one field they typed, and the one field not fenced.
	Intent string `json:"intent,omitempty"`

	Company  string `json:"company"`
	Industry string `json:"industry,omitempty"`
	// Description is the one line a person wrote about what this company does
	// (core 0203). Short, human, and the fastest way for a draft to sound like
	// it knows who it is writing to.
	Description string `json:"description,omitempty"`

	Recipient RecipientIn `json:"recipient"`
	// Deal is the opportunity the message is about, when the caller named one.
	Deal *DealIn `json:"deal,omitempty"`
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Stage       string `json:"stage,omitempty"`
	AmountMinor int64  `json:"amount_minor,omitempty"`
	Currency    string `json:"currency,omitempty"`
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
}

// draftInputActivities bounds how much of the conversation the draft reads.
// A follow-up is about the last exchange, not the relationship's history; a
// longer window costs prefill and buys older news.
const draftInputActivities = 6

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
	return in, nil
}

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
