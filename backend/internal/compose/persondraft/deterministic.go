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
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/personcontext"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
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

// The subject, from the best topic the input has, in the correspondence's own
// language. Only a real thread subject earns a reply prefix: a deal name or an
// employer is a topic this side chose, and "Re:" on one claims a thread that
// does not exist.
func deterministicSubject(in Input) string {
	lang, band := in.Envelope.Lang(), in.Envelope.Band()
	if in.Deal != nil {
		return draftfloor.Subject(lang, band, in.Deal.Name, false)
	}
	// Only a message THEY sent us is a thread to reply to. Our own last
	// outbound carries a subject too, and "Re:" on it replies to ourselves.
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return draftfloor.Subject(lang, band, in.Recent[0].Subject, in.Recent[0].Inbound)
	}
	return draftfloor.Subject(lang, band, in.Recipient.Employer, false)
}

// The body: a greeting, where the conversation stands, the one thing there is
// to say, a question. Each part is skipped rather than padded when the input
// has nothing for it.
//
// No sign-off: the composer knows who is signed in and adds their name, and a
// server that guessed would sometimes sign with the wrong one.
func deterministicBody(in Input) string {
	phrases := draftfloor.For(in.Envelope.Lang(), in.Envelope.Band())

	lines := []string{greeting(in), ""}
	if phrases.Opener != "" {
		lines = append(lines, phrases.Opener, "")
	}
	if opener := deterministicOpener(in); opener != "" {
		lines = append(lines, opener, "")
	}
	return strings.Join(append(lines, phrases.Ask), "\n")
}

func greeting(in Input) string {
	return draftfloor.Greeting(in.Envelope.Lang(), in.Envelope.Band(), in.Recipient.FirstName)
}

// The one sentence of substance, from the highest-ranked input that has
// something to say: what this person SAID outranks the deal it was said about,
// which outranks the last message anyone happened to send.
func deterministicOpener(in Input) string {
	lines := draftfloor.SubstanceFor(in.Envelope.Lang())
	if claim, ok := leadClaim(in); ok {
		return claimOpener(claim, lines)
	}
	if in.Deal != nil {
		return draftfloor.Fill(lines.Deal, dealLine(in.Deal))
	}
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return draftfloor.Fill(lines.Thread, in.Recent[0].Subject)
	}
	return ""
}

// The claim kinds a message can honestly open on, most actionable first.
//
// An OVERDUE commitment leads, and it leads for a reason worth stating: it is
// the one thing on this list the recipient is owed rather than merely
// interested in. We said we would do something by a date, the date has passed,
// and a message that opens on anything else while that is outstanding reads as
// having forgotten. It is also the archetype the whole grounding effort is for
// — the email a rep knows they should send and does not.
//
// A commitment with no due date, or one not yet due, stays out. "We said we
// would look into it" is not a reason to write today, and leading on it invents
// an urgency nobody agreed to.
//
// Below it the order is unchanged: an open question and an objection are both
// things the reader is waiting on US for, and a priority is what they told us
// matters.
var openingClaimKinds = []string{"open_question", "objection", "priority"}

// ourCommitment is the claim kind recording something WE said we would do.
//
// Derived from the contract enum rather than spelled as a literal, which is not
// a style point here: this rule first shipped keyed on "commitment", a kind the
// contract never emits, so the branch could not fire on any real record and the
// test that "proved" it fabricated the same missing kind.
//
// Their commitments are deliberately excluded. "You said you would send the
// signed order" is a real fact and a different message from "we owe you the
// scope document" - chasing them is not what this rule is for, and the prompt
// beside it says the overdue item is ours.
var ourCommitment = string(crmcontracts.CommitmentOurs)

// leadClaim picks the claim the opener refers to: the first one of the
// highest-ranked kind present. The 360 hands claims over newest-first, so
// within a kind the first is the most recent.
func leadClaim(in Input) (ClaimIn, bool) {
	// An overdue promise outranks every kind below: it is the only one the
	// recipient is owed, and the newest is the one most likely to still matter.
	for _, claim := range in.Claims {
		if claim.Kind == ourCommitment && claim.Overdue {
			return claim, true
		}
	}
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
func claimOpener(claim ClaimIn, lines draftfloor.Substance) string {
	switch claim.Kind {
	case ourCommitment:
		return draftfloor.Fill(lines.Commitment, claim.Body)
	case "open_question":
		return draftfloor.Fill(lines.OpenQuestion, claim.Body)
	case "objection":
		return draftfloor.Fill(lines.Objection, claim.Body)
	default:
		return draftfloor.Fill(lines.Priority, claim.Body)
	}
}

// dealLine names the deal the way a sentence would, with the money only when
// the record carries a currency for it.
func dealLine(deal *DealIn) string {
	spoken := personcontext.SpokenAmount(deal.AmountMinor, deal.Currency)
	if spoken == "" {
		return deal.Name
	}
	return deal.Name + " (" + spoken + ")"
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
