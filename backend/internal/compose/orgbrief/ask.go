// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// Ask Margince: the prepared questions on the company view.
//
// The question is CHOSEN, not typed. Each prepared question names the slice
// of the account its answer may be written from, which is what lets every
// sentence carry a citation the reader can open. A text box would need
// retrieval that can prove what it did NOT find — and a box that quietly
// answered from a subset would read exactly like one that searched
// everything, which is the failure worth avoiding rather than shipping.
//
// Everything below reuses the brief's machinery: the same per-viewer input,
// the same nonce fence around text written outside this workspace, the same
// grounding filter, and the same deterministic floor when no model lane is
// configured. An answer differs from a brief only in which facts it selects
// and what it is asked to say about them.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The prepared questions, derived from the contract's enum rather than
// re-spelled — a rename upstream then fails to compile here instead of
// leaving a question nobody can ask.
var (
	askWhatsOpen    = crmcontracts.OrganizationQuestionWhatsOpen
	askMeetingPrep  = crmcontracts.OrganizationQuestionMeetingPrep
	askWhatsChanged = crmcontracts.OrganizationQuestionWhatsChanged
)

// askInstruction is what each question asks the writer to do. It is the whole
// difference between the three answers, so it is stated once per question and
// nowhere else.
var askInstruction = map[crmcontracts.OrganizationQuestion]string{
	askWhatsOpen: "Answer what is currently open on this account: the open deals with their stage and amount, and the open tasks. " +
		"Do not speculate about what will close.",
	askMeetingPrep: "Answer what the reader needs before a meeting with this account: who the known contacts are, " +
		"where the pipeline stands, and whether anything is waiting for a reply. Do not invent an agenda.",
	askWhatsChanged: "Answer what has moved on this account recently, newest first, using only the timeline entries in the summary. " +
		"Do not describe a trend the entries do not show.",
}

// ParseQuestion validates the requested question.
//
// An unknown question is a 422 rather than a default, because silently
// answering a different question than the one asked is indistinguishable
// from answering the one asked badly.
func ParseQuestion(raw crmcontracts.OrganizationQuestion) (crmcontracts.OrganizationQuestion, error) {
	if _, prepared := askInstruction[raw]; !prepared {
		return "", httperr.Validation("question", "unsupported",
			fmt.Sprintf("ask one of the prepared questions: %s, %s or %s",
				askWhatsOpen, askMeetingPrep, askWhatsChanged))
	}
	return raw, nil
}

// askSystem is the shared prompt. The per-question instruction is appended,
// so the grounding rules are stated once and cannot drift between questions.
const askSystem = `You answer one question about one account in a salesperson's CRM, from a JSON summary of that account.
Return ONLY a JSON object: {"sentences":[{"text":"...","evidence":[{"entity_type":"deal|activity|person|organization","entity_id":"..."}]}]}.
Answer in one to four sentences, plainly, in the reader's second person where natural.
State only what the summary states. Never infer a cause, a mood, an intent or a next step it does not contain.
Cite the ids the summary gave you; a sentence about the account itself cites the organization.
Put ids ONLY in evidence. An id must never appear in a sentence's text — the reader sees the text, and an id there is unreadable.
Write one claim per sentence, and cite the ONE record that sentence is about. Three records worth naming are three sentences.
If the summary does not answer the question, return an empty sentences array rather than a sentence that talks around it.
If the summary names sections_omitted, say nothing about those subjects at all — the reader is not allowed to see them.`

func askSystemFor(question crmcontracts.OrganizationQuestion, fence promptfence.Fence) string {
	return askSystem + "\n" + askInstruction[question] + "\n" + fence.Rule("account summary")
}

// AskRequest builds the one request a prepared question sends. Exported for
// the same reason BriefRequest is: the certification case must issue the
// request production issues, because a rebuilt copy stays green through the
// change that breaks the original.
// The company profile is withheld here for the same reason BriefRequest
// withholds it: those statements are approved prose, and none of the three
// prepared questions asks about them. A model that had read them could
// paraphrase one into an answer about something else entirely, cited to the
// organization and so accepted by the grounding check.
func AskRequest(question crmcontracts.OrganizationQuestion, in Input) model.Request {
	return groundedRequest(func(fence promptfence.Fence) string {
		return askSystemFor(question, fence)
	}, in.withoutProfile())
}

// Answer writes the answer to one prepared question. lane may be nil, which
// is not an error state: it is the deployment saying this role runs no model,
// and the deterministic floor is the answer.
func Answer(
	ctx context.Context, lane Completer, raw crmcontracts.OrganizationQuestion, orgID string, in Input,
) ([]Sentence, crmcontracts.WrittenBy, error) {
	// Validated HERE, not only in the service: this is exported, so a second
	// caller must not be able to reach the writers with a question the package
	// does not answer. It is what makes deterministicAnswer's default branch
	// unreachable through any path rather than through the paths that remember.
	question, err := ParseQuestion(raw)
	if err != nil {
		return nil, "", err
	}
	deterministic := deterministicAnswer(question, orgID, in)
	if lane == nil {
		return deterministic, crmcontracts.Deterministic, nil
	}
	written, modelErr := answerWithModel(ctx, lane, question, orgID, in)
	if modelErr != nil {
		// The declared degrade posture, not a swallowed error: a model that is
		// unavailable, over budget, or answering unparseable JSON must not take
		// the answer down with it, and generated_by reports which the reader
		// got.
		//nolint:nilerr // on_budget_exhausted: degrade — the fallback IS the answer, and generated_by reports it
		return deterministic, crmcontracts.Deterministic, nil
	}
	return written, crmcontracts.Model, nil
}

func answerWithModel(
	ctx context.Context, lane Completer, question crmcontracts.OrganizationQuestion, orgID string, in Input,
) ([]Sentence, error) {
	resp, err := lane.Complete(ctx, AskRequest(question, in))
	if err != nil {
		return nil, err
	}
	kept, err := ParseBrief(resp.Text, orgID, in)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		// A reply that grounded nothing is not the same as "the account has no
		// answer to this": the deterministic floor knows what the summary
		// carries, so it answers instead.
		return nil, errors.New("the answer cited nothing in the account")
	}
	return kept, nil
}

// deterministicAnswer answers without a model, from the same input. Each
// question reads only its own slice of the account, so the floor and the model
// path answer the same question from the same facts.
//
// An empty result is a real outcome, not a failure: a question whose records
// this caller cannot see has no answer, and saying nothing is more honest than
// a sentence written around the gap.
//
// Unexported on purpose, and reached only through Answer, which validates its
// question first — so a question this switch does not handle cannot arrive.
func deterministicAnswer(question crmcontracts.OrganizationQuestion, orgID string, in Input) []Sentence {
	var answered []Sentence
	switch question {
	case askWhatsOpen:
		answered = openAnswer(in)
	case askMeetingPrep:
		answered = prepAnswer(orgID, in)
	case askWhatsChanged:
		answered = changedAnswer(in)
	default:
		// Unreachable: Answer validates against askInstruction, and
		// TestEveryPreparedQuestionCarriesItsOwnInstruction reads the contract's
		// own enum to prove that map covers every declared question. Returning
		// nothing rather than guessing keeps the promise that an answer is
		// written from the records it cites.
		return nil
	}
	return claims.Dedupe(answered)
}

// openAnswer lists what is open, one record per sentence.
//
// The count sentence comes first and states the TRUE total, then each deal and
// each task gets its own sentence and its own citation. The count is not
// dropped when the list is short: it carries the money — the pipeline total and
// what the account has won — which no per-deal line states.
func openAnswer(in Input) []Sentence {
	sentences := make([]Sentence, 0, 2*listedRecords+2)
	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{Text: pipelineLine(in), Evidence: leadDealEvidence(in)})
		sentences = append(sentences,
			perRecordSentences(in.OpenDeals, citeDeal, dealID, openDealLine)...)
	}
	if len(in.OpenTasks) > 0 {
		// A single task needs no count in front of it: its own sentence names
		// it and says when it is due, which is everything the count would add.
		if len(in.OpenTasks) > 1 {
			sentences = append(sentences, openTasksLine(in.OpenTasks))
		}
		sentences = append(sentences,
			perRecordSentences(in.OpenTasks, citeActivity, taskID, openTaskLine)...)
	}
	return sentences
}

func prepAnswer(orgID string, in Input) []Sentence {
	sentences := make([]Sentence, 0, listedRecords+4)
	sentences = append(sentences, Sentence{
		Text:     identityLine(in),
		Evidence: []Evidence{{EntityType: citeOrganization, EntityID: orgID}},
	})
	if len(in.Contacts) > 0 {
		if len(in.Contacts) > 1 {
			sentences = append(sentences, Sentence{
				Text: plural(len(in.Contacts), "known contact") + ".",
				// The count names nobody, so it cites the contact the list
				// starts with and gives the reader somewhere to open.
				Evidence: []Evidence{{EntityType: citePerson, EntityID: in.Contacts[0].ID}},
			})
		}
		sentences = append(sentences,
			perRecordSentences(in.Contacts, citePerson, contactID, contactLine)...)
	}
	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{Text: pipelineLine(in), Evidence: leadDealEvidence(in)})
	}
	if len(in.Recent) > 0 {
		sentences = append(sentences, Sentence{
			Text:     lastTouchLine(in.Recent[0]),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.Recent[0].ID}},
		})
	}
	return sentences
}

// changedAnswer walks the timeline newest-first. Each entry is its own
// sentence citing itself, so the reader can open the one they care about
// instead of trusting a rolled-up count.
func changedAnswer(in Input) []Sentence {
	const mostRecent = 3
	sentences := make([]Sentence, 0, mostRecent)
	for i, act := range in.Recent {
		if i >= mostRecent {
			break
		}
		sentences = append(sentences, Sentence{
			Text:     lastTouchLine(act),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: act.ID}},
		})
	}
	return sentences
}

func taskID(task TaskIn) string { return task.ID }

func contactID(contact NamedIn) string { return contact.ID }

func contactLine(contact NamedIn) string {
	return fmt.Sprintf("Known contact: %s.", contact.Name)
}

// openTasksLine counts the open tasks and anchors the count on the one that is
// due first.
//
// The count is the TRUE total even when the per-task sentences below it stop at
// listedRecords, because a reader told "5 open tasks" who has nine is worse off
// than one told nothing.
func openTasksLine(tasks []TaskIn) Sentence {
	earliest := earliestDue(tasks)
	text := plural(len(tasks), "open task") + "."
	if due := shortDate(earliest.Due); due != "" {
		text = fmt.Sprintf("%s, the earliest due %s.", plural(len(tasks), "open task"), due)
	}
	return Sentence{Text: text, Evidence: []Evidence{{EntityType: citeActivity, EntityID: earliest.ID}}}
}

// earliestDue picks the task the count sentence is anchored on: the one due
// first, or the one the list starts with when none carries a due date. TaskIn.Due
// is RFC3339 in UTC, so comparing the strings compares the instants.
func earliestDue(tasks []TaskIn) TaskIn {
	earliest := tasks[0]
	for _, task := range tasks[1:] {
		if task.Due == "" {
			continue
		}
		if earliest.Due == "" || task.Due < earliest.Due {
			earliest = task
		}
	}
	return earliest
}

func openTaskLine(task TaskIn) string {
	// The subject is quoted for the same reason an activity's is: a task can be
	// raised from mail this workspace did not write, and it must read as theirs.
	if due := shortDate(task.Due); due != "" {
		return fmt.Sprintf("Open task: %q, due %s.", task.Name, due)
	}
	return fmt.Sprintf("Open task: %q.", task.Name)
}

func openDealLine(deal DealIn) string {
	line := "Open deal: " + deal.Name
	if deal.Stage != "" {
		line += ", " + deal.Stage
	}
	if amount := renderedAmount(deal.AmountMinor, deal.Currency); amount != "" {
		// The same rendering the prompt gets, not a fresh /100 of the minor
		// integer: dividing understates every zero-decimal currency a
		// hundredfold, and two renderings of one number are two chances to
		// disagree. The card formats money properly; this text is the fallback.
		line += ", " + amount + " " + deal.Currency
	}
	if deal.Stalled {
		line += ", stalled"
	}
	return line + "."
}
