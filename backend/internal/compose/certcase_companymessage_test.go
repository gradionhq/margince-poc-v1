// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the onboarding company-message case owes the certification lane: it
// issues the request the setup conversation issues — the replayed turns, the
// wizard's own state and the clicked clarify option included — it judges the
// reply with the gate that conversation judges it with, and it separates a reply
// the gate refuses from one that answers the wrong thing.
//
// The gate is the interesting part of this site, and it is why several tests
// below run the SAME reply against two conversations that differ in one wizard
// input. next_required_field and a clicked option are grants: with them a change
// is one the administrator asked for, without them the identical reply is a
// confused-deputy action arriving at the confirm-first queue wearing their
// request. A case whose gate was built from anything but the fixture it sent
// would report the wrong verdict on both.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

// The replies these runs are about. Written as text rather than marshalled so a
// malformed reply is as expressible as a well-formed one.
const (
	companyMessageNameReply = `{"kind":"correction","message":"Noted — Acme Robotics it is.",` +
		`"proposed_changes":[{"field":"display_name","value":"Acme Robotics","reason":"You just told me.",` +
		`"source_ids":[]}],"source_ids":[]}`
	companyMessageLegalNameReply = `{"kind":"correction","message":"I will use the registered name you picked.",` +
		`"proposed_changes":[{"field":"legal_name","value":"Acme Robotics GmbH","reason":"You selected it.",` +
		`"source_ids":[]}],"source_ids":[]}`
	companyMessageIndustryReply = `{"kind":"correction","message":"I will use Robotics manufacturing.",` +
		`"proposed_changes":[{"field":"industry","value":"Robotics manufacturing","reason":"You asked for it.",` +
		`"source_ids":[]}],"source_ids":[]}`
	companyMessageUnaskedChangeReply = `{"kind":"correction","message":"I have set your industry too.",` +
		`"proposed_changes":[{"field":"industry","value":"Warehouse robotics","reason":"The product page implies it.",` +
		`"source_ids":["S2"]}],"source_ids":["S2"]}`
)

// companyMessageDossier is what the crawl grounded, numbered the way
// companyReadEvidenceSet numbers it.
func companyMessageDossier() []companyReadEvidence {
	return []companyReadEvidence{
		{
			ID: "S1", Kind: "legal_entity", Field: "legal_identity",
			Value: "Acme Robotics GmbH · Werkstr. 1 · HRB 12345",
			Quote: "Acme Robotics GmbH, Werkstr. 1, HRB 12345", URL: "https://acme.example/imprint",
		},
		{
			ID: "S2", Kind: "profile_field", Field: "offer_summary", Value: "Warehouse robotics",
			Quote: "We build warehouse robotics.", URL: "https://acme.example/product",
		},
	}
}

// companyMessageFixture is the wizard's opening question answered with a bare
// value. Nothing in the message names a field: next_required_field is the only
// reason this reads as a correction to display_name at all, which is what makes
// it the turn this site exists to be measured on.
func companyMessageFixture() onboardingCompanyMessageFixture {
	return onboardingCompanyMessageFixture{
		Message: "Acme Robotics",
		History: []crmcontracts.CompanySiteReadConversationTurn{
			{Role: crmcontracts.CompanySiteReadConversationTurnRoleUser, Message: "Let's set the company up."},
			{
				Role:    crmcontracts.CompanySiteReadConversationTurnRoleAssistant,
				Message: "What name should I show for your company?",
			},
		},
		Conversation: onboardingConversationContext{
			Dossier:           companyMessageDossier(),
			NextRequired:      fieldDisplayName,
			RemainingRequired: []string{fieldDisplayName, fieldOfferSummary, fieldICP},
		},
		Locale: string(crmcontracts.OnboardingCompanyLocaleEN),
	}
}

// companyMessageClickFixture is the same conversation answered by clicking a
// clarify option instead of typing. "That one." names no field and carries no
// value: the grant is the click, and nothing else in this turn.
func companyMessageClickFixture() onboardingCompanyMessageFixture {
	fixture := companyMessageFixture()
	fixture.Message = "That one."
	fixture.SelectedOption = &crmcontracts.OnboardingClarifySelection{
		ClarifyId: "clarify:legal_name:1", Field: fieldLegalName, Value: "Acme Robotics GmbH",
	}
	return fixture
}

// companyMessageChangeRequestFixture is the wizard finished and the
// administrator asking for a named change outright. Nothing is required any
// more, so the completion plan grants nothing: the message names its own field,
// which is the only reason a change to it is authorized at all.
func companyMessageChangeRequestFixture() onboardingCompanyMessageFixture {
	fixture := companyMessageFixture()
	fixture.Message = "Please change our industry to Robotics manufacturing."
	fixture.Conversation.CurrentDraft = identity.OnboardingCompanyDraft{
		DisplayName:  stringPointer("Acme Robotics"),
		OfferSummary: stringPointer("Warehouse robotics"),
		ICP:          stringPointer("Mid-market logistics operators"),
	}
	fixture.Conversation.NextRequired = ""
	fixture.Conversation.RemainingRequired = nil
	return fixture
}

func companyMessageFixtureJSON(t *testing.T, f onboardingCompanyMessageFixture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// companyMessageExpectationJSON is what the corpus asserts, encoded as the
// corpus will carry it — beside the fixture, never inside it.
func companyMessageExpectationJSON(t *testing.T, kind string, changes map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(companyConversationExpectation{Kind: kind, Changes: changes})
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func companyMessageNameExpectation(t *testing.T) json.RawMessage {
	t.Helper()
	return companyMessageExpectationJSON(t, "correction", map[string]string{fieldDisplayName: "Acme Robotics"})
}

func runCompanyMessageCase(
	t *testing.T, fixture onboardingCompanyMessageFixture, expected json.RawMessage, reply string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := onboardingCompanyMessageCases{}.Prepare(companyMessageFixtureJSON(t, fixture), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), companyReadCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// companyMessageOutcomeCase is one canned reply, the conversation it answers and
// the verdict the case owes it.
type companyMessageOutcomeCase struct {
	name       string
	fixture    onboardingCompanyMessageFixture
	expected   json.RawMessage
	reply      string
	wantResult string
	wantDetail string
}

func runCompanyMessageOutcomeCases(t *testing.T, cases []companyMessageOutcomeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runCompanyMessageCase(t, tc.fixture, tc.expected, tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A refused reply is reported in the gate's own words, because those words are
// what turns a reliability drop into a diagnosis. Every one of these is a reply
// the administrator must never be shown — the opposite fix from a wrong answer,
// which is why the two are never one number.
func TestCompanyMessageCaseReportsWhatTheGateRefused(t *testing.T) {
	clickWithoutTheClick := companyMessageClickFixture()
	clickWithoutTheClick.SelectedOption = nil

	runCompanyMessageOutcomeCases(t, []companyMessageOutcomeCase{
		{
			// The change nobody asked for. It is grounded, well formed and
			// plausible — and it would arrive at the human's confirm-first queue
			// wearing the administrator's own request.
			name:       "a change the conversation never authorized",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      companyMessageUnaskedChangeReply,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `proposes "industry" without an administrator change request`,
		},
		{
			// The SAME reply the click authorizes below, in the same conversation
			// with the click taken away. "That one." grants nothing by itself, so a
			// case that carried the selection into the request but not into the gate
			// would accept this one too.
			name:       "the clicked change, in the turn without the click",
			fixture:    clickWithoutTheClick,
			expected:   companyMessageExpectationJSON(t, "answer", nil),
			reply:      companyMessageLegalNameReply,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `proposes "legal_name" without an administrator change request`,
		},
		{
			// The plan's grant is for the value the administrator just supplied, not
			// for the field at large — so a dossier value they never uttered, handed
			// back as their answer, is refused rather than merely wrong.
			name:     "the plan's field carrying a value the administrator never said",
			fixture:  companyMessageFixture(),
			expected: companyMessageNameExpectation(t),
			reply: `{"kind":"correction","message":"Noted.","proposed_changes":[{"field":"display_name",` +
				`"value":"Acme Robotics GmbH · Werkstr. 1 · HRB 12345","reason":"Stated.","source_ids":["S1"]}],` +
				`"source_ids":["S1"]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `proposes "display_name" without an administrator change request`,
		},
		{
			name:     "a change cited to a source the dossier does not hold",
			fixture:  companyMessageFixture(),
			expected: companyMessageNameExpectation(t),
			reply: `{"kind":"correction","message":"Noted.","proposed_changes":[{"field":"display_name",` +
				`"value":"Acme Robotics","reason":"Stated.","source_ids":["S9"]}],"source_ids":["S9"]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "cites unknown source",
		},
		{
			name:     "a kind that may not propose changes proposing one",
			fixture:  companyMessageFixture(),
			expected: companyMessageNameExpectation(t),
			reply: `{"kind":"answer","message":"Here it is.","proposed_changes":[{"field":"display_name",` +
				`"value":"Acme Robotics","reason":"Stated.","source_ids":[]}],"source_ids":[]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "may not propose changes",
		},
		{
			name:       "a kind outside the closed vocabulary",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      `{"kind":"celebration","message":"Great name!","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unsupported response kind",
		},
		{
			name:       "a reply that is not the required JSON",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      "I have saved Acme Robotics.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
	})
}

// The other half: replies the gate is content with, judged against what the
// scenario says this conversation should have produced. A wrong answer here is a
// measurement of the model, not a defect in the reply.
func TestCompanyMessageCaseSeparatesTheRightAnswerFromAWellFormedWrongOne(t *testing.T) {
	runCompanyMessageOutcomeCases(t, []companyMessageOutcomeCase{
		{
			// The bare answer to the wizard's own question. Only
			// next_required_field makes this a correction to display_name.
			name:       "the value the completion plan asked for",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      companyMessageNameReply,
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The clicked option, granted verbatim and to nothing else.
			name:     "the change the administrator clicked",
			fixture:  companyMessageClickFixture(),
			expected: companyMessageExpectationJSON(t, "correction", map[string]string{fieldLegalName: "Acme Robotics GmbH"}),
			reply:    companyMessageLegalNameReply,

			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// Well formed, gate-clean and in the wrong register: a measurement of
			// the model, not a defect in the reply.
			name:       "a well-formed reply in a register the scenario disagrees with",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      `{"kind":"answer","message":"That is a good name.","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `answered as "answer"`,
		},
		{
			// A named change request authorizes the field, not a value — so the gate
			// proves only that the value is grounded, and only the scenario knows
			// which grounded value was the right one.
			name:       "a grounded change carrying the wrong value",
			fixture:    companyMessageChangeRequestFixture(),
			expected:   companyMessageExpectationJSON(t, "correction", map[string]string{fieldIndustry: "Robotics manufacturing"}),
			reply:      companyMessageUnaskedChangeReply,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "industry reads",
		},
		{
			name:       "a correction that proposes nothing at all",
			fixture:    companyMessageFixture(),
			expected:   companyMessageNameExpectation(t),
			reply:      `{"kind":"correction","message":"I understand.","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "no surviving display_name",
		},
	})
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion.
func TestCompanyMessageFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	always := map[string]bool{"message": true, "history": true, "conversation": true, "locale": true}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(companyMessageFixtureJSON(t, companyMessageFixture()), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	for name := range fields {
		if !always[name] {
			t.Errorf("the fixture carries %q, which the company answer path is not given", name)
		}
	}
	for name := range always {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the company answer path always has", name)
		}
	}
	// The click is the one thing the path is given only sometimes, so it is the
	// one key a fixture may add — and it may add nothing else.
	var clicked map[string]json.RawMessage
	if err := json.Unmarshal(companyMessageFixtureJSON(t, companyMessageClickFixture()), &clicked); err != nil {
		t.Fatalf("decoding the click fixture: %v", err)
	}
	for name := range clicked {
		if !always[name] && name != "selected_option" {
			t.Errorf("the click fixture carries %q, which the company answer path is not given", name)
		}
	}
	if _, present := clicked["selected_option"]; !present {
		t.Error("the click fixture drops the selection, which is the whole of what makes it a click")
	}
}
