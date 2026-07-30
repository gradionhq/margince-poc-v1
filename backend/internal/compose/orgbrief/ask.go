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
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The prepared questions, derived from the contract's enum rather than
// re-spelled — a rename upstream then fails to compile here instead of
// leaving a question nobody can ask.
var (
	askWhatsOpen    = crmcontracts.WhatsOpen
	askMeetingPrep  = crmcontracts.MeetingPrep
	askWhatsChanged = crmcontracts.WhatsChanged
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
If the summary does not answer the question, return an empty sentences array rather than a sentence that talks around it.
If the summary names sections_omitted, say nothing about those subjects at all — the reader is not allowed to see them.`

func askSystemFor(question crmcontracts.OrganizationQuestion, fence promptfence.Fence) string {
	return askSystem + "\n" + askInstruction[question] + "\n" + fence.Rule("account summary")
}

// AskRequest builds the one request a prepared question sends. Exported for
// the same reason BriefRequest is: the certification case must issue the
// request production issues, because a rebuilt copy stays green through the
// change that breaks the original.
func AskRequest(question crmcontracts.OrganizationQuestion, in Input) model.Request {
	return groundedRequest(func(fence promptfence.Fence) string {
		return askSystemFor(question, fence)
	}, in)
}

// Answer writes the answer to one prepared question. lane may be nil, which
// is not an error state: it is the deployment saying this role runs no model,
// and the deterministic floor is the answer.
func Answer(
	ctx context.Context, lane Completer, question crmcontracts.OrganizationQuestion, orgID string, in Input,
) ([]Sentence, crmcontracts.WrittenBy, error) {
	deterministic := deterministicAnswer(question, orgID, in)
	if lane == nil {
		return deterministic, crmcontracts.Deterministic, nil
	}
	written, err := answerWithModel(ctx, lane, question, orgID, in)
	if err != nil {
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
// Unexported on purpose. The only way in is Answer, which every caller reaches
// through ParseQuestion — so a question this switch does not handle cannot
// arrive, and the function never has to answer "nothing" to a bad argument.
func deterministicAnswer(question crmcontracts.OrganizationQuestion, orgID string, in Input) []Sentence {
	switch question {
	case askWhatsOpen:
		return openAnswer(in)
	case askMeetingPrep:
		return prepAnswer(orgID, in)
	case askWhatsChanged:
		return changedAnswer(in)
	default:
		// A question declared in the contract, accepted by ParseQuestion, and
		// not wired here. Returning nothing rather than guessing keeps the
		// promise that an answer is written from the records it cites; the
		// completeness of askInstruction is what stops it happening (a question
		// missing from that map is refused at the door).
		return nil
	}
}

func openAnswer(in Input) []Sentence {
	sentences := make([]Sentence, 0, 2)
	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{Text: pipelineLine(in), Evidence: dealEvidence(in)})
	}
	if len(in.OpenTasks) > 0 {
		sentences = append(sentences, Sentence{
			Text:     fmt.Sprintf("Open tasks: %s.", strings.Join(namesOf(in.OpenTasks), ", ")),
			Evidence: namedEvidence(in.OpenTasks, citeActivity),
		})
	}
	return sentences
}

func prepAnswer(orgID string, in Input) []Sentence {
	sentences := make([]Sentence, 0, 3)
	sentences = append(sentences, Sentence{
		Text:     identityLine(in),
		Evidence: []Evidence{{EntityType: citeOrganization, EntityID: orgID}},
	})
	if len(in.Contacts) > 0 {
		sentences = append(sentences, Sentence{
			Text:     fmt.Sprintf("Known contacts: %s.", strings.Join(namesOf(in.Contacts), ", ")),
			Evidence: namedEvidence(in.Contacts, citePerson),
		})
	}
	if len(in.OpenDeals) > 0 {
		sentences = append(sentences, Sentence{Text: pipelineLine(in), Evidence: dealEvidence(in)})
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

func namesOf(records []NamedIn) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.Name)
	}
	return out
}

func namedEvidence(records []NamedIn, entityType string) []Evidence {
	out := make([]Evidence, 0, len(records))
	for _, record := range records {
		out = append(out, Evidence{EntityType: entityType, EntityID: record.ID})
	}
	return out
}
