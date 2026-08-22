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
// The folding itself lives in fold.go.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
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

// draftInputSnippetRunes bounds how much of the newest inbound message the
// draft reads.
//
// Enough for the opening of a real business email — a greeting, the reason for
// writing, and the ask. Past that an email is detail, which a reply asks about
// rather than repeats, and every rune of it is prompt cost on every draft.
const draftInputSnippetRunes = 400

// Input is the person, narrowed to what an outbound message can honestly stand
// on. It is a projection of the caller's own 360 — nothing here re-queries, so
// anything absent is absent because that caller may not see it.
type Input struct {
	// Intent is the caller's own steering ("shorter", "ask for Tuesday"). The
	// one field they typed, and the one field not fenced.
	Intent string `json:"intent,omitempty"`

	// Envelope is the correspondence this draft is written into: its language,
	// how long it has been silent, the current time and who is signing it.
	// Server-derived, never read out of the counterparty's own text.
	Envelope draftfloor.Envelope `json:"envelope"`

	Recipient RecipientIn `json:"recipient"`
	// Deal is the open opportunity this person sits on, when the caller can see
	// deals and one is open.
	Deal *DealIn `json:"deal,omitempty"`
	// Project is the body of work the message is about, when the caller named
	// one. The view it is folded from is already scoped to it, so Recent and
	// Claims below describe this project's correspondence, not another's.
	Project *ProjectIn `json:"project,omitempty"`
	// Claims are the things this person actually said — what they asked for,
	// promised, or objected to. They outrank the conversation below them: a
	// message is context for writing, where a claim is a reason to write.
	Claims []ClaimIn `json:"claims,omitempty"`
	Recent []ActIn   `json:"recent,omitempty"`
	// Meeting is the next scheduled meeting THIS PERSON is on, when there is
	// one. A draft that asks for a call when one is already booked reads as not
	// knowing, and it is the most concrete thing a follow-up can refer to.
	//
	// Only meetings they attend. One they are NOT on is somebody else's
	// calendar, and naming it in a message to them discloses a meeting they
	// were not invited to.
	Meeting *MeetingIn `json:"meeting,omitempty"`
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
	// rows the next-steps section shows — so the draft can say "two things
	// are still open" without naming ones it was not shown.
	OpenCommitments int `json:"open_commitments"`
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
	// Due is when this was promised for, RFC3339, empty when nothing was
	// promised by a date. It is the difference between "we said we would send
	// the scope" and "we said we would send the scope by the 25th, and it is
	// the 11th of August" — one is a note, the other is a reason to write
	// today, and the drafter cannot tell them apart without the date.
	Due string `json:"due,omitempty"`
	// Overdue says the due date has passed. Derived here rather than left to
	// the model, which has "now" and a date and would still have to do the
	// arithmetic in prose.
	Overdue bool `json:"overdue,omitempty"`
	// SourceID is the activity the claim was read from — carried so a reason
	// about a claim cites the conversation the reader can open rather than the
	// derived row, which has no page.
	SourceID string `json:"source_id"`
}

// MeetingIn is the next meeting this person is on.
//
// It carries no attendee list, and that absence is the rule rather than an
// omission: who ELSE is in the room is internal context about the account, and
// a draft naming the other attendees to the recipient tells them who we are
// also talking to.
type MeetingIn struct {
	// Subject as scheduled, empty when the meeting has none.
	Subject string `json:"subject,omitempty"`
	// StartsAt is RFC3339 UTC. The drafter is told WHEN so it can write "next
	// Tuesday" rather than a timestamp, which is why the envelope's own clock
	// is what it compares against.
	StartsAt string `json:"starts_at"`
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
	// Snippet is the opening of the newest INBOUND message on this person's
	// timeline. A subject line says a message happened; the words say what it
	// was about, and a draft grounded in subjects alone can only gesture at a
	// conversation it never read.
	//
	// It is what the message SAYS, not a claim about who wrote it. An activity
	// reaches a person through activity_link, which records what a message
	// concerns rather than who authored it — a colleague's introduction that
	// copies a prospect is linked to that prospect — and the 360 does not carry
	// participants, so authorship is not knowable here. The prompt beside it
	// says "a message on this thread" rather than "what they wrote", because
	// the stronger sentence would be one the data cannot support.
	//
	// The opening rather than the whole message: an email says why it was sent
	// in its first lines and spends the rest on detail, and the detail is what
	// a reply should ask about rather than repeat back.
	Snippet string `json:"snippet,omitempty"`
}

// String is the debug rendering, never the prompt payload — the prompt sends
// JSON so the model reads a structure rather than prose it might imitate.
func (in Input) String() string {
	return fmt.Sprintf("persondraft{to:%q deal:%v claims:%d}",
		in.Recipient.Name, in.Deal != nil, len(in.Claims))
}
