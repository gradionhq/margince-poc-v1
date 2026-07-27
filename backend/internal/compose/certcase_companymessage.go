// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for cold_start/company_message — the conversation an
// administrator has with Margince while setting the company up.
//
// It certifies the shipped path rather than a description of it: the request
// comes from onboardingCompanyAnswerRequest and the reply is judged by the gate
// newOnboardingCompanyGate builds, both of which the assistant uses for the same
// turn. A case that rebuilt either would measure a copy, and a copy stays green
// through the change that breaks the original.
//
// This site is MULTI-TURN, and the kind names the conversation rather than the
// number of calls: the model is stateless, so the whole prior conversation is
// replayed as messages of the ONE request this site sends. The case replays
// exactly the turns the transport would have replayed, through
// companyReadConversation — the same mapping, which is also the bound on how many
// turns a call may carry.
//
// What separates it from the dossier conversation it shares a validator with is
// the wizard state, and that state reaches the AUTHORIZATION as well as the
// prompt:
//
//   - next_required_field is the question the product just asked, so a bare
//     answer that names no field at all — "Acme Robotics" — is a correction to
//     THAT field and to nothing else.
//   - a clicked clarify option grants exactly its field with its value verbatim,
//     which is the strongest explicit instruction this conversation has.
//
// Both are grants the dossier conversation has no way to make, and a grant is
// what stands between a change the administrator asked for and one that arrives
// at the confirm-first queue wearing their request. So the case's gate is built
// from the fixture it sent, and never from anything else.
//
// The conversation context arrives assembled rather than as the identity and
// people stores it came from, because the certified thing is the prompt built
// from it, not the database read that produced it. What that read guarantees
// about it — the dossier's numbering and bounds, and the completion plan the
// draft implies — is enforced at Prepare instead, so a fixture outside it
// describes a call the product cannot make.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// companyMessageSite names this site in every refusal it writes, so a corpus
// author reading one knows which scenario to open.
const companyMessageSite = "cold_start/company_message"

// onboardingCompanyMessageFixture is ONE turn of the company-setup conversation,
// in exactly what the transport hands the answer path: what the administrator
// just said, the conversation it follows, the context the server assembled, the
// locale to answer in, and — when the human answered by clicking rather than
// typing — the clarify option they picked.
//
// The turns are carried in the transport's own wire shape so the case bounds
// them the way the transport bounds them, and the selection in the contract's,
// because it is echoed back by the browser rather than composed here.
type onboardingCompanyMessageFixture struct {
	Message        string                                         `json:"message"`
	History        []crmcontracts.CompanySiteReadConversationTurn `json:"history"`
	Conversation   onboardingConversationContext                  `json:"conversation"`
	Locale         string                                         `json:"locale"`
	SelectedOption *crmcontracts.OnboardingClarifySelection       `json:"selected_option,omitempty"`
}

// onboardingCompanyMessageCases serves the site that answers an administrator
// setting their company up.
type onboardingCompanyMessageCases struct{}

func (onboardingCompanyMessageCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskColdStart,
		Variant: "company_message",
		Kind:    ai.SiteKindMultiTurn,
	}
}

// Prepare turns one setup turn and what the scenario expects of it into a
// runnable case, deriving the gate from the same message, history, context and
// selection the request is built from — which is the whole reason Prepare exists.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (onboardingCompanyMessageCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f onboardingCompanyMessageFixture
	if err := decodeCompanyConversationScenario(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", companyMessageSite, err)
	}
	if err := refuseUnsendableCompanyTurn(f); err != nil {
		return nil, err
	}
	if err := refuseUnassemblableSetupContext(f.Conversation); err != nil {
		return nil, err
	}
	history, err := companyReadConversation(&f.History)
	if err != nil {
		return nil, fmt.Errorf("%s: the fixture's history is not one the transport accepts: %w", companyMessageSite, err)
	}
	message := strings.TrimSpace(f.Message)
	conversation := f.Conversation
	// The server always builds the plan from the draft, and an omitted list and
	// an empty one do not marshal alike — so the plan the model is shown is the
	// derived one, having refused a fixture that claimed a different one.
	conversation.RemainingRequired = remainingOnboardingFields(conversation.CurrentDraft)
	gate := newOnboardingCompanyGate(message, history, conversation, f.SelectedOption)
	want, err := readCompanyConversationExpectation(companyMessageSite, expected, gate)
	if err != nil {
		return nil, err
	}
	return &onboardingCompanyMessageCase{
		message: message, history: history, conversation: conversation,
		locale: f.Locale, selection: f.SelectedOption, gate: gate, expected: want,
	}, nil
}

// refuseUnsendableCompanyTurn names a turn the onboarding transport would never
// have let through. The locale decides which language the prompt demands, the
// message is trimmed and bounded at decode time, the dossier is built by
// companyReadEvidenceSet, and a selection is checked field-by-field before it
// ever reaches the answer path.
//
// What it cannot check is that the selection is one the SERVER offered:
// verifySelectedOption re-derives the open clarifications from the persisted read
// to decide that, which is the database read this fixture stands in for. So a
// fixture asserts that the human clicked a server-authored option, and what is
// certified is the prompt and the grant built from it.
func refuseUnsendableCompanyTurn(f onboardingCompanyMessageFixture) error {
	if !crmcontracts.OnboardingCompanyMessageRequestLocale(f.Locale).Valid() {
		return fmt.Errorf(
			"%s: the fixture asks for the locale %q, which onboarding never answers in", companyMessageSite, f.Locale)
	}
	if err := refuseUnsendableCompanyMessage(companyMessageSite, f.Message); err != nil {
		return err
	}
	if err := refuseUnassemblableDossier(companyMessageSite, f.Conversation.Dossier); err != nil {
		return err
	}
	if f.SelectedOption == nil {
		return nil
	}
	selection := *f.SelectedOption
	if strings.TrimSpace(selection.ClarifyId) == "" {
		return fmt.Errorf(
			"%s: the fixture's selected option echoes no clarify id, and the transport refuses one that does not",
			companyMessageSite)
	}
	if !crmcontracts.CompanySiteReadSuggestedChangeField(strings.TrimSpace(selection.Field)).Valid() {
		return fmt.Errorf(
			"%s: the fixture's selected option names %q, which is not a company field a clarification can offer",
			companyMessageSite, selection.Field)
	}
	if strings.TrimSpace(selection.Value) == "" {
		return fmt.Errorf(
			"%s: the fixture's selected option carries no value, and the grant it authorizes is exactly that value",
			companyMessageSite)
	}
	return nil
}

// refuseUnassemblableSetupContext holds the fixture's context block to what the
// server could have assembled around it. The draft is bounded field by field at
// decode time, and the completion plan is DERIVED from that draft — so a fixture
// naming a next_required_field its own draft has already filled certifies an
// authorization the product cannot grant: next_required_field is what lets a bare
// value with no field name in it correct a field.
func refuseUnassemblableSetupContext(conversation onboardingConversationContext) error {
	values := onboardingDraftValues(conversation.CurrentDraft)
	// Sorted so a draft with two oversized fields names the same one every time.
	for _, field := range slices.Sorted(maps.Keys(values)) {
		value := values[field]
		if value == nil {
			continue
		}
		if n := len([]rune(*value)); n > onboardingCompanyDraftMaxRunes {
			return fmt.Errorf(
				"%s: the fixture's draft carries %d characters of %s, and the transport bounds every draft field at %d",
				companyMessageSite, n, field, onboardingCompanyDraftMaxRunes,
			)
		}
	}
	remaining := remainingOnboardingFields(conversation.CurrentDraft)
	if !slices.Equal(conversation.RemainingRequired, remaining) {
		return fmt.Errorf(
			"%s: the fixture's remaining required fields are %v, and the server derives %v from the draft it carries",
			companyMessageSite, conversation.RemainingRequired, remaining,
		)
	}
	next := ""
	if len(remaining) > 0 {
		next = remaining[0]
	}
	if conversation.NextRequired != next {
		return fmt.Errorf(
			"%s: the fixture asks for %q next, and the server asks for %q from the draft it carries",
			companyMessageSite, conversation.NextRequired, next,
		)
	}
	return nil
}

// onboardingCompanyMessageCase is one setup turn ready to be answered, closed
// over the gate built from that same turn.
type onboardingCompanyMessageCase struct {
	message      string
	history      []model.Message
	conversation onboardingConversationContext
	locale       string
	selection    *crmcontracts.OnboardingClarifySelection
	gate         companyReadGate
	expected     companyConversationExpectation
}

// Run issues the one request this site sends, the replayed conversation inside
// it. It sends it bare: production wraps the same request in the shape-retry
// when the brain supports one, and a case that retried would certify the answer
// a model gives after being told to try again rather than the answer it gives.
func (c *onboardingCompanyMessageCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req, err := onboardingCompanyAnswerRequest(c.message, c.history, c.conversation, c.locale, c.selection)
	if err != nil {
		return aitasks.Trace{}, fmt.Errorf("%s: %w", companyMessageSite, err)
	}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("%s: %w", companyMessageSite, err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate judges the reply with the gate this turn was sent under, in the
// answer path's own order.
func (c *onboardingCompanyMessageCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	return evaluateCompanyConversationReply(c.gate, c.expected, trace)
}
