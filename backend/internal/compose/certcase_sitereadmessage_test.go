// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the company-read message case owes the certification lane: it issues the
// request the dossier conversation issues — the replayed turns and the dossier
// included — it judges the reply with the gate that conversation judges it with,
// and it separates a reply the gate refuses from one that answers the wrong
// thing.
//
// The gate is the interesting part of this site, and it is why several tests
// below run the SAME reply against two conversations. A proposed company change
// is legitimate in the conversation that asked for it and a confused-deputy
// action in the one that did not, so a case whose gate was built from anything
// but the fixture it sent would report the wrong verdict on both.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The reply an administrator's correction earns, and the same reply in a
// conversation where nobody asked for anything. Written as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
const (
	companyReadCorrectionReply = `{"kind":"correction","message":"I have noted the registered name.",` +
		`"proposed_changes":[{"field":"legal_name","value":"Acme Robotics GmbH","reason":"The imprint states it.",` +
		`"source_ids":["S1"]}],"source_ids":["S1"]}`
	companyReadUnaskedChangeReply = `{"kind":"correction","message":"I have set your industry too.",` +
		`"proposed_changes":[{"field":"industry","value":"Warehouse robotics","reason":"The product page implies it.",` +
		`"source_ids":["S2"]}],"source_ids":["S2"]}`
)

// companyReadCompleterStub answers with the canned reply a run is about. What
// the model was asked reaches the assertions through the trace, which is where
// the record and the canary gate read it from too.
type companyReadCompleterStub struct{ reply string }

func (s companyReadCompleterStub) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// companyReadCorrectionFixture is an administrator correcting the registered
// name, over a dossier that grounds it. The turn before is what makes the
// correction a follow-up rather than an opening line.
func companyReadCorrectionFixture() companyReadMessageFixture {
	return companyReadMessageFixture{
		Message: "Please correct the legal name to Acme Robotics GmbH.",
		History: []crmcontracts.CompanySiteReadConversationTurn{
			{Role: crmcontracts.CompanySiteReadConversationTurnRoleUser, Message: "What did you find in the imprint?"},
			{
				Role:    crmcontracts.CompanySiteReadConversationTurnRoleAssistant,
				Message: "The imprint names Acme Robotics GmbH, HRB 12345.",
			},
		},
		Evidence: []companyReadEvidence{
			{
				ID: "S1", Kind: "legal_entity", Field: "legal_identity",
				Value: "Acme Robotics GmbH · Werkstr. 1 · HRB 12345",
				Quote: "Acme Robotics GmbH, Werkstr. 1, HRB 12345", URL: "https://acme.example/imprint",
			},
			{
				ID: "S2", Kind: "profile_field", Field: "offer_summary", Value: "Warehouse robotics",
				Quote: "We build warehouse robotics.", URL: "https://acme.example/product",
			},
		},
	}
}

// companyReadQuestionFixture is the same dossier under a question. Nothing in
// this conversation authorizes a change to anything.
func companyReadQuestionFixture() companyReadMessageFixture {
	fixture := companyReadCorrectionFixture()
	fixture.Message = "What does the imprint say about our registered name?"
	return fixture
}

func companyReadFixtureJSON(t *testing.T, f companyReadMessageFixture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// companyReadExpectationJSON is what the corpus asserts, encoded as the corpus
// will carry it — beside the fixture, never inside it.
func companyReadExpectationJSON(t *testing.T, kind string, changes map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(companyConversationExpectation{Kind: kind, Changes: changes})
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func companyReadCorrectionExpectation(t *testing.T) json.RawMessage {
	t.Helper()
	return companyReadExpectationJSON(t, "correction", map[string]string{"legal_name": "Acme Robotics GmbH"})
}

func runCompanyReadCase(
	t *testing.T, fixture companyReadMessageFixture, expected json.RawMessage, reply string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := companyReadMessageCases{}.Prepare(companyReadFixtureJSON(t, fixture), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), companyReadCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// companyReadOutcomeCase is one canned reply, the conversation it answers and
// the verdict the case owes it.
type companyReadOutcomeCase struct {
	name       string
	fixture    companyReadMessageFixture
	expected   json.RawMessage
	reply      string
	wantResult string
	wantDetail string
}

func runCompanyReadOutcomeCases(t *testing.T, cases []companyReadOutcomeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runCompanyReadCase(t, tc.fixture, tc.expected, tc.reply)
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
func TestCompanyReadMessageCaseReportsWhatTheGateRefused(t *testing.T) {
	runCompanyReadOutcomeCases(t, []companyReadOutcomeCase{
		{
			// The change nobody asked for. It is grounded, well formed and
			// plausible — and it would arrive at the human's confirm-first queue
			// wearing the administrator's own request. The gate is what stops it,
			// so this is the reply the site exists to be measured on.
			name:       "a change the conversation never authorized",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      companyReadUnaskedChangeReply,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `proposes "industry" without an administrator change request`,
		},
		{
			// The SAME reply that is accepted above, under a conversation that
			// asked a question instead of asking for a change. A gate built from
			// anything but the fixture it sent would accept this one too.
			name:       "the accepted correction, answered to a question",
			fixture:    companyReadQuestionFixture(),
			expected:   companyReadExpectationJSON(t, "answer", nil),
			reply:      companyReadCorrectionReply,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `proposes "legal_name" without an administrator change request`,
		},
		{
			name:     "a change cited to a source the dossier does not hold",
			fixture:  companyReadCorrectionFixture(),
			expected: companyReadCorrectionExpectation(t),
			reply: `{"kind":"correction","message":"Found it.","proposed_changes":[{"field":"legal_name",` +
				`"value":"Acme Robotics GmbH","reason":"Stated.","source_ids":["S9"]}],"source_ids":["S9"]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "cites unknown source",
		},
		{
			name:     "a value no cited evidence supports",
			fixture:  companyReadCorrectionFixture(),
			expected: companyReadCorrectionExpectation(t),
			reply: `{"kind":"correction","message":"Found it.","proposed_changes":[{"field":"legal_name",` +
				`"value":"Acme Robotics AG","reason":"Stated.","source_ids":["S1"]}],"source_ids":["S1"]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "not supported by its cited evidence",
		},
		{
			name:     "a change to a field outside the onboarding vocabulary",
			fixture:  companyReadCorrectionFixture(),
			expected: companyReadCorrectionExpectation(t),
			reply: `{"kind":"correction","message":"Done.","proposed_changes":[{"field":"website",` +
				`"value":"acme.example","reason":"Stated.","source_ids":[]}],"source_ids":[]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unsupported field",
		},
		{
			name:     "a kind that may not propose changes proposing one",
			fixture:  companyReadCorrectionFixture(),
			expected: companyReadCorrectionExpectation(t),
			reply: `{"kind":"answer","message":"Here it is.","proposed_changes":[{"field":"legal_name",` +
				`"value":"Acme Robotics GmbH","reason":"Stated.","source_ids":["S1"]}],"source_ids":["S1"]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "may not propose changes",
		},
		{
			name:       "a kind outside the closed vocabulary",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      `{"kind":"celebration","message":"Nice imprint!","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unsupported response kind",
		},
		{
			name:       "a reply that is not the required JSON",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      "I have corrected the legal name.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
	})
}

// The other half: replies the gate is content with, judged against what the
// scenario says this conversation should have produced. A wrong answer here is
// a measurement of the model, not a defect in the reply.
func TestCompanyReadMessageCaseSeparatesTheRightAnswerFromAWellFormedWrongOne(t *testing.T) {
	runCompanyReadOutcomeCases(t, []companyReadOutcomeCase{
		{
			name:       "the expected correction, grounded and asked for",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      companyReadCorrectionReply,
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// Well formed, gate-clean and in the wrong register: a measurement of
			// the model, not a defect in the reply.
			name:       "a well-formed reply in a register the scenario disagrees with",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      `{"kind":"answer","message":"The imprint names Acme Robotics GmbH.","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `answered as "answer"`,
		},
		{
			// The gate proves a value is grounded; only the scenario knows which
			// grounded value was the right one. Handing back the whole evidence
			// line as the legal name passes the gate and is still wrong.
			name:     "a grounded change carrying the wrong value",
			fixture:  companyReadCorrectionFixture(),
			expected: companyReadCorrectionExpectation(t),
			reply: `{"kind":"correction","message":"Noted.","proposed_changes":[{"field":"legal_name",` +
				`"value":"Acme Robotics GmbH · Werkstr. 1 · HRB 12345","reason":"Stated.","source_ids":["S1"]}],` +
				`"source_ids":["S1"]}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "legal_name reads",
		},
		{
			name:       "a correction that proposes nothing at all",
			fixture:    companyReadCorrectionFixture(),
			expected:   companyReadCorrectionExpectation(t),
			reply:      `{"kind":"correction","message":"I understand.","proposed_changes":[],"source_ids":[]}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "no surviving legal_name",
		},
	})
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same fixture, answered by the same
// model text, must produce the same request and the same verdict in the
// administrator's real transport as in the case. Anything the answer path
// derives between the fixture and the gate — the dossier index, the
// administrator's statements, the authorization — is caught here if the case
// derives it differently.
func TestCompanyReadMessageCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name string
		// wantRefusedBy is production's own refusal, empty when production
		// accepts. The case owes the same verdict in the same words: a detail
		// that paraphrases is a diagnosis a reader has to translate back.
		reply         string
		wantResult    string
		wantRefusedBy string
	}{
		{
			name: "a change the administrator asked for", reply: companyReadCorrectionReply,
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "a change nobody authorized", reply: companyReadUnaskedChangeReply,
			wantResult:    aitasks.OutcomeInvalid,
			wantRefusedBy: `compose: company read answer proposes "industry" without an administrator change request`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := companyReadCorrectionFixture()
			history, err := companyReadConversation(&fixture.History)
			if err != nil {
				t.Fatalf("mapping the fixture's history: %v", err)
			}
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			engine := deepReadEngine{brain: brain}
			_, productionErr := engine.answerCompanySiteRead(
				context.Background(), strings.TrimSpace(fixture.Message), history, fixture.Evidence,
			)

			outcome, trace := runCompanyReadCase(t, fixture, companyReadCorrectionExpectation(t), tc.reply)

			productionRefusal := ""
			if productionErr != nil {
				productionRefusal = productionErr.Error()
			}
			if productionRefusal != tc.wantRefusedBy {
				t.Fatalf("production refusal = %q, want %q", productionRefusal, tc.wantRefusedBy)
			}
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if outcome.Detail != tc.wantRefusedBy {
				t.Errorf("the case reports %q, want production's own words %q", outcome.Detail, tc.wantRefusedBy)
			}
			assertSameCompanyReadRequest(t, brain.request, trace.Requests[0])
		})
	}
}

// assertSameCompanyReadRequest compares two requests for the same turn. The
// fence marker is minted per call, so it is normalised away — every other byte
// of a request the certification lane claims production sends must match the
// one production sent.
func assertSameCompanyReadRequest(t *testing.T, production, certified model.Request) {
	t.Helper()
	normalize := func(req model.Request) model.Request {
		marker, declared := promptfence.MarkerIn(req.System)
		if !declared {
			t.Fatalf("the request declares no data boundary: %q", req.System)
		}
		out := req
		out.System = strings.ReplaceAll(req.System, marker, "MARKER")
		out.Messages = make([]model.Message, len(req.Messages))
		for i, message := range req.Messages {
			out.Messages[i] = model.Message{Role: message.Role, Content: strings.ReplaceAll(message.Content, marker, "MARKER")}
		}
		return out
	}
	production, certified = normalize(production), normalize(certified)
	if production.System != certified.System {
		t.Errorf("the certified system prompt is not production's:\n%q\n%q", certified.System, production.System)
	}
	if len(production.Messages) != len(certified.Messages) {
		t.Fatalf("the certified request carries %d turns, production sent %d",
			len(certified.Messages), len(production.Messages))
	}
	for i, message := range production.Messages {
		if certified.Messages[i] != message {
			t.Errorf("certified turn %d = %+v, production sent %+v", i, certified.Messages[i], message)
		}
	}
	if certified.MaxTokens != production.MaxTokens || string(certified.ResponseSchema) != string(production.ResponseSchema) ||
		certified.SecretStripper == nil {
		t.Errorf("the certified request lost the governed bounds production sends: %+v", certified)
	}
}

// This site is multi-turn, and the turns are what makes a scenario about a
// follow-up mean anything: the model is stateless, so a case that dropped the
// history would certify an opening line on a scenario written about a
// correction that only reads as one because of the turn before it.
func TestCompanyReadMessageCaseReplaysTheConversationItWasGiven(t *testing.T) {
	fixture := companyReadCorrectionFixture()

	_, trace := runCompanyReadCase(t, fixture, companyReadCorrectionExpectation(t), companyReadCorrectionReply)

	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one call this site sends", len(trace.Requests))
	}
	messages := trace.Requests[0].Messages
	if len(messages) != len(fixture.History)+2 {
		t.Fatalf("the request carries %d turns, want the dossier, %d replayed turns and the current message",
			len(messages), len(fixture.History))
	}
	for i, turn := range fixture.History {
		replayed := messages[i+1]
		if replayed.Role != string(turn.Role) || replayed.Content != turn.Message {
			t.Errorf("replayed turn %d = %+v, want role %q and %q", i+1, replayed, turn.Role, turn.Message)
		}
	}
	if last := messages[len(messages)-1]; last.Content != fixture.Message {
		t.Errorf("the current message is not the last turn: %+v", last)
	}
	marker, declared := promptfence.MarkerIn(trace.Requests[0].System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", trace.Requests[0].System)
	}
	if !strings.HasPrefix(messages[0].Content, "<"+marker+">") {
		t.Errorf("the dossier is not inside the boundary the prompt declares:\n%s", messages[0].Content)
	}
	if !strings.Contains(messages[0].Content, "Werkstr. 1") {
		t.Errorf("the fixture's dossier never reached the request:\n%s", messages[0].Content)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the validator to read")
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion.
func TestCompanyReadMessageFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(companyReadFixtureJSON(t, companyReadCorrectionFixture()), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"message": true, "history": true, "evidence": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the company-read path is not given", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the company-read path always has", name)
		}
	}
}
