// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

// The no-model floor.
//
// It is not an error path. A deployment that runs no model lane, or a workspace
// whose budget is spent, still has a rep who pressed "Write email" — and a
// short honest opener they edit is a better answer than a spinner that ends in
// a refusal.
//
// What it will not do is imitate the model. It states only what the input gave
// it, in plain sentences, and asks one question. No figure it was not handed,
// no claim about what the recipient thinks, no invented urgency.

import (
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// Draft is the written message plus what it was written from, before the wire
// shape. Shared by both writers so the floor cannot drift into a different
// answer than the lane's.
type Draft struct {
	Subject   string
	Body      string
	To        []string
	Reasoning []Reason
}

// Reason is one grounding input, named for the reader.
type Reason struct {
	Kind  crmcontracts.AccountDraftReasonKind
	Label string
	// EntityType and EntityID are both set or both empty: a citation is a pair,
	// and half of one points at nothing.
	EntityType string
	EntityID   string
}

// Deterministic writes the floor draft.
func Deterministic(in Input) Draft {
	return Draft{
		Subject:   deterministicSubject(in),
		Body:      deterministicBody(in),
		To:        toAddresses(in),
		Reasoning: deterministicReasons(in),
	}
}

func deterministicSubject(in Input) string {
	if in.Deal != nil {
		return in.Deal.Name
	}
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return "Re: " + in.Recent[0].Subject
	}
	if in.Recipient.Employer != "" {
		return "Following up · " + in.Recipient.Employer
	}
	return "Following up"
}

// The body: a greeting, the one thing there is to say, a question. Each part is
// skipped rather than padded when the input has nothing for it.
//
// No sign-off: the composer knows who is signed in and adds their name, and a
// server that guessed would sometimes sign with the wrong one.
func deterministicBody(in Input) string {
	lines := []string{greeting(in), ""}
	if opener := deterministicOpener(in); opener != "" {
		lines = append(lines, opener, "")
	}
	return strings.Join(append(lines, "Would a short call this week suit you?"), "\n")
}

func greeting(in Input) string {
	if in.Recipient.FirstName == "" {
		return "Hello,"
	}
	return "Hi " + in.Recipient.FirstName + ","
}

// The one sentence of substance, from the highest-ranked input that has
// something to say: what this person SAID outranks the deal it was said about,
// which outranks the last message anyone happened to send.
func deterministicOpener(in Input) string {
	if claim, ok := leadClaim(in); ok {
		return claimOpener(claim)
	}
	if in.Deal != nil {
		return "I wanted to pick up where we left off on " + dealLine(in.Deal) + "."
	}
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return "I wanted to follow up on " + in.Recent[0].Subject + "."
	}
	return ""
}

// The claim kinds a first message can honestly open on, most actionable first.
// An open question and an objection are both things the reader is waiting on us
// for; a priority is what they told us matters. The other kinds — our own
// commitments, decisions already taken — are not openers for a message TO them.
var openingClaimKinds = []string{"open_question", "objection", "priority"}

// leadClaim picks the claim the opener refers to: the first one of the
// highest-ranked kind present. The 360 hands claims over newest-first, so
// within a kind the first is the most recent.
func leadClaim(in Input) (ClaimIn, bool) {
	for _, kind := range openingClaimKinds {
		for _, claim := range in.Claims {
			if claim.Kind == kind {
				return claim, true
			}
		}
	}
	return ClaimIn{}, false
}

// claimOpener writes the sentence in the register the claim's kind calls for.
// An objection is something we owe them an answer on; a question is something
// they asked; a priority is something they said matters. Rendering all three
// the same way would put "you objected to" in a message about a preference.
func claimOpener(claim ClaimIn) string {
	switch claim.Kind {
	case "open_question":
		return "I wanted to come back to you on " + claim.Body + "."
	case "objection":
		return "I still owe you an answer on " + claim.Body + "."
	default:
		return "I know " + claim.Body + " matters to you, and I wanted to come back to it."
	}
}

// dealLine names the deal the way a sentence would, with the money only when
// the record carries a currency for it.
func dealLine(deal *DealIn) string {
	spoken := spokenAmount(deal.AmountMinor, deal.Currency)
	if spoken == "" {
		return deal.Name
	}
	return deal.Name + " (" + spoken + ")"
}

// spokenAmount renders a deal's value the way somebody would SAY it in a
// sentence: "€95k", not "95000.00 EUR". The exact figure belongs on the deal
// card, where a reader is checking a number; here it is one clause of a
// sentence, and the full decimal spelling reads as a database field pasted into
// prose.
//
// A zero amount and an amount with no currency are both rendered as nothing: a
// figure whose scale the reader has to guess is worse in an outbound message
// than no figure at all.
func spokenAmount(minor int64, currency string) string {
	if minor == 0 || currency == "" {
		return ""
	}
	symbol := map[string]string{"EUR": "€", "USD": "$", "GBP": "£"}[currency]
	if symbol == "" {
		symbol = currency + " "
	}
	major := minor / 100
	if major >= 1000 {
		return fmt.Sprintf("%s%dk", symbol, major/1000)
	}
	return fmt.Sprintf("%s%d", symbol, major)
}

// The floor cites what it actually used, so a reader gets the same "Based on"
// line either writer produced. It cannot cite what it did not read.
//
// A claim is cited by its SOURCE activity, not by its own id: the claim row has
// no page, and a chip has to open something.
func deterministicReasons(in Input) []Reason {
	reasons := []Reason{{
		Kind:       crmcontracts.AccountDraftReasonKindRecipient,
		Label:      in.Recipient.Name,
		EntityType: citePerson,
		EntityID:   in.Recipient.ID,
	}}
	claim, hasClaim := leadClaim(in)
	if hasClaim {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindCommitment,
			Label:      claim.Body,
			EntityType: citeActivity,
			EntityID:   claim.SourceID,
		})
	}
	if in.Deal != nil {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindDeal,
			Label:      in.Deal.Name,
			EntityType: citeDeal,
			EntityID:   in.Deal.ID,
		})
	}
	if !hasClaim && in.Deal == nil && len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindConversation,
			Label:      in.Recent[0].Subject,
			EntityType: citeActivity,
			EntityID:   in.Recent[0].ID,
		})
	}
	return reasons
}
