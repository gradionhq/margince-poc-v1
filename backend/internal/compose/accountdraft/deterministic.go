// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// The no-model floor.
//
// It is not an error path. A deployment that runs no model lane, or a
// workspace whose budget is spent, still has a rep who pressed "Write email" —
// and a short honest opener they edit is a better answer than a spinner that
// ends in a refusal.
//
// What it will not do is imitate the model. It states only what the summary
// gave it, in plain sentences, and asks one question. No figure it was not
// handed, no claim about what the recipient thinks, no invented urgency.

import (
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
	if in.Commitment != nil {
		return in.Commitment.Name
	}
	return "Following up · " + in.Company
}

// The body: a greeting, the one thing there is to say, a question, a sign-off.
// Each part is skipped rather than padded when the summary has nothing for it.
func deterministicBody(in Input) string {
	lines := []string{greeting(in), ""}
	if subject := deterministicOpener(in); subject != "" {
		lines = append(lines, subject, "")
	}
	// No sign-off: the composer knows who is signed in and adds their name,
	// and a server that guessed would sometimes sign with the wrong one.
	lines = append(lines, "Would a short call this week suit you?")
	return strings.Join(lines, "\n")
}

func greeting(in Input) string {
	if in.Recipient.FirstName == "" {
		return "Hello,"
	}
	return "Hi " + in.Recipient.FirstName + ","
}

// The one sentence of substance, from the highest-ranked input that has
// something to say. The order is A132's: a commitment we made outranks the
// deal it belongs to, which outranks the account in general.
func deterministicOpener(in Input) string {
	if in.Commitment != nil {
		return "I wanted to come back to you on " + in.Commitment.Name + "."
	}
	if in.Deal != nil {
		return "I wanted to pick up where we left off on " + in.Deal.Name + "."
	}
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return "I wanted to follow up on " + in.Recent[0].Subject + "."
	}
	return ""
}

// The floor cites what it actually used, so a reader gets the same "Based on"
// line either writer produced. It cannot cite what it did not read.
func deterministicReasons(in Input) []Reason {
	reasons := []Reason{{
		Kind:       crmcontracts.AccountDraftReasonKindRecipient,
		Label:      in.Recipient.Name,
		EntityType: "person",
		EntityID:   in.Recipient.ID,
	}}
	if in.Commitment != nil {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindCommitment,
			Label:      in.Commitment.Name,
			EntityType: "activity",
			EntityID:   in.Commitment.ID,
		})
	}
	if in.Deal != nil {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindDeal,
			Label:      in.Deal.Name,
			EntityType: "deal",
			EntityID:   in.Deal.ID,
		})
	}
	if in.Commitment == nil && in.Deal == nil && len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		reasons = append(reasons, Reason{
			Kind:       crmcontracts.AccountDraftReasonKindConversation,
			Label:      in.Recent[0].Subject,
			EntityType: "activity",
			EntityID:   in.Recent[0].ID,
		})
	}
	return reasons
}
