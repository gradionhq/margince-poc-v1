// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

// What one draft is written from, in the person page's own grounding order: the
// caller's intent, then who the recipient is, then the deal and the money on
// it, then what they have SAID, then what recently happened.
//
// The order is the prompt's reading order, not a preference: an instruction the
// caller typed outranks a record, and a claim the person made themselves
// outranks a message somebody merely sent them.
//
// Nothing here re-queries. Every field is folded out of the Person360 the
// caller already assembled, which is what makes the draft's scope exactly the
// reader's own scope without a second set of gates to keep in agreement.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// draftInputActivities bounds how much of the conversation the draft reads. A
// follow-up is about the last exchange, not the relationship's history; a
// longer window costs prefill and buys older news.
const draftInputActivities = 6

// draftInputClaims bounds what the draft may reach for. Past a handful the
// prompt is choosing between claims rather than writing from them, and the one
// it picks is no longer the newest.
const draftInputClaims = 6

// Input is the person, narrowed to what an outbound message can honestly stand
// on. It is a projection of the caller's own 360 — nothing here re-queries, so
// anything absent is absent because that caller may not see it.
type Input struct {
	// Intent is the caller's own steering ("shorter", "ask for Tuesday"). The
	// one field they typed, and the one field not fenced.
	Intent string `json:"intent,omitempty"`

	Recipient RecipientIn `json:"recipient"`
	// Deal is the open opportunity this person sits on, when the caller can see
	// deals and one is open.
	Deal *DealIn `json:"deal,omitempty"`
	// Claims are the things this person actually said — what they asked for,
	// promised, or objected to. They outrank the conversation below them: a
	// message is context for writing, where a claim is a reason to write.
	Claims []ClaimIn `json:"claims,omitempty"`
	Recent []ActIn   `json:"recent,omitempty"`
	// SectionsOmitted names what the caller could NOT see, so the writer stays
	// silent about those sections rather than inferring around the gap.
	SectionsOmitted []string `json:"sections_omitted,omitempty"`
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
	Employer  string `json:"employer,omitempty"`
	Email     string `json:"email,omitempty"`
	// BuyingRole is the seat this person holds on the deal, as recorded. Never
	// inferred from the title — a seat is relationship data.
	BuyingRole string `json:"buying_role,omitempty"`
	// LastInbound and LastOutbound are RFC3339 UTC, empty when that direction
	// never happened. Kept apart rather than folded into one "last touch":
	// which direction went last is the whole question a follow-up answers.
	LastInbound  string `json:"last_inbound,omitempty"`
	LastOutbound string `json:"last_outbound,omitempty"`
}

// DealIn is the open opportunity the message can refer to.
type DealIn struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stage string `json:"stage,omitempty"`
	// AmountMinor is the exact integer the column holds and what this package
	// does arithmetic on. It does NOT reach the model: MarshalJSON renders
	// `amount` from it in MAJOR units, because a prompt carrying minor units
	// once had a model read a 180,000 EUR deal as eighteen million. Dividing by
	// 100 at the point of use is wrong too — a zero-decimal currency has no
	// minor unit — so values.MajorUnits carries the ISO 4217 table.
	AmountMinor int64  `json:"-"`
	Currency    string `json:"currency,omitempty"`
	CloseDate   string `json:"close_date,omitempty"`
}

// MarshalJSON renders the deal's amount as a person would say it, derived from
// the integer at the moment it is written. Two spellings of one number that a
// caller can set independently are two numbers.
//
// An amount with no currency is not shown at all: a figure without its code is
// a number whose scale the reader has to guess.
func (d DealIn) MarshalJSON() ([]byte, error) {
	type plain DealIn // no methods, so no recursion back into this one
	amount := ""
	if d.Currency != "" {
		amount = values.MajorUnits(d.AmountMinor, d.Currency)
	}
	return json.Marshal(struct {
		plain
		Amount string `json:"amount,omitempty"`
	}{plain: plain(d), Amount: amount})
}

// ClaimIn is one thing this person said. The kind rides along because "she
// objected to X" and "she asked for X" are opposite claims about the same
// sentence, and the body alone loses which one it was.
type ClaimIn struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Body string `json:"body"`
	// SourceID is the activity the claim was read from — carried so a reason
	// about a claim cites the conversation the reader can open rather than the
	// derived row, which has no page.
	SourceID string `json:"source_id"`
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

// FromView folds the caller's 360 into the draft's input.
func FromView(view crmcontracts.Person360, req Request) Input {
	in := Input{
		Intent:          strings.TrimSpace(req.Intent),
		Recipient:       recipientOf(view),
		SectionsOmitted: omittedNames(view.SectionsOmitted),
	}
	foldCommercial(&in, view)
	foldClaims(&in, view)
	foldRecent(&in, view)
	return in
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

// foldClaims keeps the claims a message can honestly refer to. A dismissed
// claim is one a human said was never true, and writing an email from it would
// resurrect it in front of the customer.
func foldClaims(in *Input, view crmcontracts.Person360) {
	if view.Claims == nil {
		return
	}
	for _, claim := range *view.Claims {
		if len(in.Claims) == draftInputClaims {
			break
		}
		if claim.Status == crmcontracts.ConversationClaimStatusDismissed {
			continue
		}
		in.Claims = append(in.Claims, ClaimIn{
			ID:       claim.Id.String(),
			Kind:     string(claim.Kind),
			Body:     claim.Body,
			SourceID: claim.SourceActivityId.String(),
		})
	}
}

func foldRecent(in *Input, view crmcontracts.Person360) {
	if view.Activities == nil {
		return
	}
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
		in.Recent = append(in.Recent, folded)
	}
}

// omittedNames renders the withheld sections as plain strings for the writer.
// The contract types them as an enum; the draft only needs the names.
func omittedNames(omitted []crmcontracts.Person360SectionsOmitted) []string {
	if len(omitted) == 0 {
		return nil
	}
	out := make([]string, 0, len(omitted))
	for _, section := range omitted {
		out = append(out, string(section))
	}
	return out
}

// stamp renders an optional instant in one fixed format, so two timestamps
// compare as strings the way the instants they name compare.
func stamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// String is the debug rendering, never the prompt payload — the prompt sends
// JSON so the model reads a structure rather than prose it might imitate.
func (in Input) String() string {
	return fmt.Sprintf("persondraft{to:%q deal:%v claims:%d}",
		in.Recipient.Name, in.Deal != nil, len(in.Claims))
}
