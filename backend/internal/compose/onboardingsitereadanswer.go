// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two halves of the dossier conversation's model call: the request it
// sends, and the gate it judges the answer with. They sit together, and apart
// from the transport that calls them, because they are one seam with two
// callers — the administrator's live conversation and the certification lane
// that measures it. A lane that rebuilt either half would measure a copy, and a
// copy stays green through the change that breaks the original.

import (
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// companyReadAnswerRequest builds the ONE call this site sends: the dossier
// inside the boundary the prompt declares, the conversation so far replayed
// after it, and the administrator's current message last. It is a pure
// function of what the transport supplies, so the certification lane issues
// the request that ships rather than a re-creation of it — and a re-creation
// stays green through the change that breaks the original.
//
// The model is stateless, so the prior turns are messages of THIS request
// rather than a session it remembers, and they sit between the dossier and the
// current message because that is the order they happened in: a follow-up
// reference resolves against whichever turn precedes it.
//
// The fence is minted per request. A boundary a previous turn was shown is one
// whoever wrote that turn can spell, and the rule this prompt states — that the
// dossier is evidence and never instruction — can only be stated about a region
// the model can tell apart.
func companyReadAnswerRequest(message string, history []model.Message, evidence []companyReadEvidence) (model.Request, error) {
	fence := promptfence.New()
	contextJSON, err := json.Marshal(struct {
		Dossier []companyReadEvidence `json:"dossier_evidence"`
	}{Dossier: evidence})
	if err != nil {
		return model.Request{}, err
	}
	messages := make([]model.Message, 0, len(history)+2)
	messages = append(messages, model.Message{Role: chatRoleUser, Content: fence.Wrap(string(contextJSON))})
	messages = append(messages, history...)
	messages = append(messages, model.Message{Role: chatRoleUser, Content: message})
	return model.Request{
		System:    companyReadMessageSystem + "\n" + fence.Rule("dossier evidence and application state"),
		Messages:  messages,
		MaxTokens: ai.ReasoningOutputMaxTokens, ResponseSchema: companyReadMessageSchema,
		SecretStripper: ai.NewSecretStripper(),
	}, nil
}

// companyReadGate is the company-read validator closed over the three things it
// judges a reply against: the dossier the model was shown, every statement the
// administrator has made in this conversation, and the authorization those
// statements grant.
//
// It is one constructor rather than three call-site derivations because all
// three come from the same message, history and evidence the request is built
// from. A caller that assembled them itself could hand the validator a
// conversation other than the one it sent — a gate judging a message the model
// never saw refuses changes the administrator did ask for, and admits changes
// nobody asked for at all.
type companyReadGate struct {
	known         map[string]companyReadEvidence
	statements    string
	authorization companyChangeAuthorization
}

func newCompanyReadGate(message string, history []model.Message, evidence []companyReadEvidence) companyReadGate {
	known := make(map[string]companyReadEvidence, len(evidence))
	for _, source := range evidence {
		known[source.ID] = source
	}
	return companyReadGate{
		known:         known,
		statements:    administratorConversation(history, message),
		authorization: newCompanyChangeAuthorization(message, history, ""),
	}
}

// validate judges the model's raw text, which is the shape the shape-retry
// takes; validateReply judges an already-parsed reply, which is the shape the
// answer path re-checks after unfencing. Both are the same rules.
func (g companyReadGate) validate(text string) error {
	return validateCompanyReadReply(text, g.known, g.statements, g.authorization)
}

func (g companyReadGate) validateReply(reply companyReadModelReply) error {
	return validateCompanyReadReplyValue(reply, g.known, g.statements, g.authorization)
}
