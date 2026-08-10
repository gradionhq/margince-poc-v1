// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personbrief

// The brief without a model.
//
// Every sentence cites the record it came from, exactly as a model-written one
// would, so the card renders and behaves identically whichever wrote it. That
// is the point of the floor: a workspace with no model lane gets a plainer
// brief, not a blank card where the brief should be, and `generated_by` says
// which it is rather than passing one off as the other.

import (
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
)

// The citable record types. A brief may only point at things the reader can
// open, and these are the ones the person page can open in place.
const (
	citePerson   = "person"
	citeActivity = "activity"
	citeDeal     = "deal"
)

// Deterministic writes the brief from the assembled input alone.
//
// The order answers the questions a reader asks in the order they ask them:
// who is this person commercially, what have they said they care about, and
// what happened last.
func Deterministic(personID string, in Input) []Sentence {
	self := []Evidence{{EntityType: citePerson, EntityID: personID}}
	sentences := make([]Sentence, 0, 4)

	sentences = append(sentences, Sentence{Text: identityLine(in), Evidence: self})

	if in.OpenDeal != nil {
		sentences = append(sentences, Sentence{
			Text:     dealLine(in),
			Evidence: []Evidence{{EntityType: citeDeal, EntityID: in.OpenDeal.ID}},
		})
	}
	if line, evidence, ok := caresAboutLine(in); ok {
		sentences = append(sentences, Sentence{Text: line, Evidence: evidence})
	}
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		sentences = append(sentences, Sentence{
			Text:     lastTouchLine(in, last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	return claims.Dedupe(sentences)
}

// identityLine says who this person is in the current commercial context —
// the first thing a reader needs and the one sentence that is always true.
func identityLine(in Input) string {
	switch {
	case in.Title != "" && in.Employer != "":
		return fmt.Sprintf("%s is %s at %s.", in.Name, in.Title, in.Employer)
	case in.Employer != "":
		return fmt.Sprintf("%s works at %s.", in.Name, in.Employer)
	case in.Title != "":
		return fmt.Sprintf("%s is %s.", in.Name, in.Title)
	default:
		return fmt.Sprintf("%s is recorded here with no title or employer.", in.Name)
	}
}

// dealLine states the commercial stake, with the seat this person holds on it.
// The role is stored relationship data — it is never inferred from a title.
func dealLine(in Input) string {
	deal := in.OpenDeal
	parts := []string{deal.Name}
	if spoken := spokenAmount(deal.AmountMinor, deal.Currency); spoken != "" {
		parts = append(parts, spoken)
	}
	if deal.Stage != "" {
		parts = append(parts, deal.Stage)
	}
	line := strings.Join(parts, " · ")
	if in.BuyingRole != "" {
		return fmt.Sprintf("They are the recorded %s on %s.", readableRole(in.BuyingRole), line)
	}
	return fmt.Sprintf("They sit on %s, with no buying role recorded.", line)
}

// readableRole turns the stored role key into words. The keys are a naming
// convention rather than an enum, so an unrecognized one is rendered as it was
// stored — inventing a label for a role nobody defined would be a claim.
func readableRole(role string) string {
	return strings.ReplaceAll(role, "_", " ")
}

// spokenAmount renders a deal's value the way somebody would SAY it in a
// sentence: "€95k", not "95000.00 EUR".
//
// The exact figure belongs on the deal card, where a reader is checking a
// number. Here it is one clause of a sentence about a relationship, and the
// full decimal spelling reads as a database field pasted into prose.
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

// caresAboutLine names what this person has explicitly said matters, citing the
// conversation it was said in rather than the derived claim row — the reader
// checks a sentence against what was actually written.
func caresAboutLine(in Input) (string, []Evidence, bool) {
	priorities := claimsOfKind(in, "priority")
	objections := claimsOfKind(in, "objection")
	if len(priorities) == 0 && len(objections) == 0 {
		return "", nil, false
	}
	var parts []string
	var evidence []Evidence
	if len(priorities) > 0 {
		parts = append(parts, fmt.Sprintf("focused on %s", priorities[0].Body))
		evidence = append(evidence, Evidence{EntityType: citeActivity, EntityID: priorities[0].SourceID})
	}
	if len(objections) > 0 {
		parts = append(parts, fmt.Sprintf("with %s still unresolved", objections[0].Body))
		evidence = append(evidence, Evidence{EntityType: citeActivity, EntityID: objections[0].SourceID})
	}
	return fmt.Sprintf("They are %s.", strings.Join(parts, ", ")), evidence, true
}

func claimsOfKind(in Input, kind string) []ClaimIn {
	var out []ClaimIn
	for _, claim := range in.Claims {
		if claim.Kind == kind {
			out = append(out, claim)
		}
	}
	return out
}

// lastTouchLine says which direction went last, because that is the whole
// question: a contact we wrote to a fortnight ago with no reply and one who
// wrote to us this morning have the same last-touch date and opposite meanings.
func lastTouchLine(in Input, last ActIn) string {
	subject := last.Subject
	if subject == "" {
		subject = last.Kind
	}
	switch {
	case in.LastInbound != "" && in.LastInbound > in.LastOutbound:
		return fmt.Sprintf("They wrote last, about %q, and it is unanswered.", subject)
	case in.LastOutbound != "":
		return fmt.Sprintf("We wrote last, about %q, with no reply yet.", subject)
	default:
		return fmt.Sprintf("The last thing captured was %q.", subject)
	}
}
