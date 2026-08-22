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

// The subject, from the best topic the account has, in the correspondence's own
// language. Nothing here is a thread subject - this drafter opens a NEW
// conversation - so none of it earns a reply prefix.
func deterministicSubject(in Input) string {
	lang, band := in.Envelope.Lang(), in.Envelope.Band()
	if in.Deal != nil {
		return draftfloor.Subject(lang, band, in.Deal.Name, false)
	}
	if in.Commitment != nil {
		return draftfloor.Subject(lang, band, in.Commitment.Name, false)
	}
	// A project outranks the account in general: a message about one body of
	// work is titled after the work, and its key is what the recipient's own
	// filing recognises.
	if in.Project != nil {
		return draftfloor.Subject(lang, band, projectTopic(in.Project), false)
	}
	return draftfloor.Subject(lang, band, in.Company, false)
}

// projectTopic is the project as a subject line names it: "ERP-27 ERP rollout"
// when the project has a key, the bare name when it has none.
func projectTopic(project *ProjectIn) string {
	if project.Key == "" {
		return project.Name
	}
	return project.Key + " " + project.Name
}

// The body: a greeting, where the conversation stands, the one thing there is
// to say, a question. Each part is skipped rather than padded when the summary
// has nothing for it.
func deterministicBody(in Input) string {
	phrases := draftfloor.For(in.Envelope.Lang(), in.Envelope.Band())

	lines := []string{greeting(in), ""}
	if phrases.Opener != "" {
		lines = append(lines, phrases.Opener, "")
	}
	if subject := deterministicOpener(in); subject != "" {
		lines = append(lines, subject, "")
	}
	// No sign-off: the composer knows who is signed in and adds their name,
	// and a server that guessed would sometimes sign with the wrong one.
	return strings.Join(append(lines, phrases.Ask), "\n")
}

func greeting(in Input) string {
	return draftfloor.Greeting(in.Envelope.Lang(), in.Envelope.Band(), in.Recipient.FirstName)
}

// The one sentence of substance, from the highest-ranked input that has
// something to say. The order is A132's: a commitment we made outranks the
// deal it belongs to, which outranks the account in general.
func deterministicOpener(in Input) string {
	lines := draftfloor.SubstanceFor(in.Envelope.Lang())
	if in.Commitment != nil {
		return draftfloor.Fill(lines.Commitment, in.Commitment.Name)
	}
	if in.Deal != nil {
		return draftfloor.Fill(lines.Deal, in.Deal.Name)
	}
	if len(in.Recent) > 0 && in.Recent[0].Subject != "" {
		return draftfloor.Fill(lines.Thread, in.Recent[0].Subject)
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
