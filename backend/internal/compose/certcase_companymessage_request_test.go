// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the onboarding company-message case SENDS, as against what it makes of
// the answer. The two halves are asked separately because they fail separately:
// a case that judges correctly while sending a prompt production does not send
// reports a number about a call that never happens, and every outcome test in
// the file beside this one would stay green through exactly that drift.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same fixture, answered by the same
// model text, must produce the same request and the same verdict in the
// administrator's real assistant as in the case. Anything the answer path
// derives between the fixture and the gate — the dossier index, the
// administrator's statements, the plan's direct field, the clicked option's
// grant — is caught here if the case derives it differently.
//
// The assistant's answer path reads no store, so a brain is the whole of what it
// needs: the certified turn runs against the same struct the transport calls.
func TestCompanyMessageCaseRunsWhatProductionRuns(t *testing.T) {
	clickExpectation := companyMessageExpectationJSON(
		t, "correction", map[string]string{fieldLegalName: "Acme Robotics GmbH"})
	cases := []struct {
		name     string
		fixture  onboardingCompanyMessageFixture
		expected json.RawMessage
		// wantRefusedBy is production's own refusal, empty when production
		// accepts. The case owes the same verdict in the same words: a detail
		// that paraphrases is a diagnosis a reader has to translate back.
		reply         string
		wantResult    string
		wantRefusedBy string
	}{
		{
			name: "the value the completion plan asked for", fixture: companyMessageFixture(),
			expected: companyMessageNameExpectation(t),
			reply:    companyMessageNameReply, wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "the change the administrator clicked", fixture: companyMessageClickFixture(),
			expected: clickExpectation,
			reply:    companyMessageLegalNameReply, wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "a change nobody authorized", fixture: companyMessageFixture(),
			expected:      companyMessageNameExpectation(t),
			reply:         companyMessageUnaskedChangeReply,
			wantResult:    aitasks.OutcomeInvalid,
			wantRefusedBy: `compose: company read answer proposes "industry" without an administrator change request`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history, err := companyReadConversation(&tc.fixture.History)
			if err != nil {
				t.Fatalf("mapping the fixture's history: %v", err)
			}
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			assistant := &onboardingCompanyAssistant{brain: brain}
			_, productionErr := assistant.answer(
				context.Background(), strings.TrimSpace(tc.fixture.Message), history,
				tc.fixture.Conversation, tc.fixture.Locale, tc.fixture.SelectedOption,
			)

			outcome, trace := runCompanyMessageCase(t, tc.fixture, tc.expected, tc.reply)

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

// This site is multi-turn, and the turns are what makes a scenario about a
// follow-up mean anything: the model is stateless, so a case that dropped the
// history would certify an opening line on a scenario written about an answer
// that only reads as one because of the question before it.
func TestCompanyMessageCaseReplaysTheConversationItWasGiven(t *testing.T) {
	fixture := companyMessageFixture()

	_, trace := runCompanyMessageCase(t, fixture, companyMessageNameExpectation(t), companyMessageNameReply)

	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one call this site sends", len(trace.Requests))
	}
	req := trace.Requests[0]
	if len(req.Messages) != len(fixture.History)+2 {
		t.Fatalf("the request carries %d turns, want the context block, %d replayed turns and the current message",
			len(req.Messages), len(fixture.History))
	}
	for i, turn := range fixture.History {
		replayed := req.Messages[i+1]
		if replayed.Role != string(turn.Role) || replayed.Content != turn.Message {
			t.Errorf("replayed turn %d = %+v, want role %q and %q", i+1, replayed, turn.Role, turn.Message)
		}
	}
	if last := req.Messages[len(req.Messages)-1]; last.Content != fixture.Message {
		t.Errorf("the current message is not the last turn: %+v", last)
	}
	if !strings.Contains(req.System, "Respond in en.") {
		t.Errorf("the traced request does not name the fixture's locale: %q", req.System)
	}
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	if !strings.HasPrefix(req.Messages[0].Content, "<"+marker+">") {
		t.Errorf("the context block is not inside the boundary the prompt declares:\n%s", req.Messages[0].Content)
	}
	for _, want := range []string{"Werkstr. 1", `"next_required_field":"display_name"`, `"offer_summary","icp"`} {
		if !strings.Contains(req.Messages[0].Content, want) {
			t.Errorf("the context block does not carry %s:\n%s", want, req.Messages[0].Content)
		}
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the validator to read")
	}
}

// A click reaches the model as an explicit administrator statement, because a
// bare option label leaves it guessing which exact value the human chose. It is
// a turn of the conversation, so it belongs after the turns that preceded it and
// before the message it accompanies.
func TestCompanyMessageCaseSpeaksTheClickedOptionAsAnAdministratorStatement(t *testing.T) {
	fixture := companyMessageClickFixture()

	_, trace := runCompanyMessageCase(
		t, fixture,
		companyMessageExpectationJSON(t, "correction", map[string]string{fieldLegalName: "Acme Robotics GmbH"}),
		companyMessageLegalNameReply,
	)

	messages := trace.Requests[0].Messages
	if len(messages) != len(fixture.History)+3 {
		t.Fatalf("the request carries %d turns, want the context block, %d replayed turns, the click and the message",
			len(messages), len(fixture.History))
	}
	spoken := messages[len(messages)-2]
	if spoken.Role != chatRoleUser {
		t.Errorf("the click is spoken as %q, want an administrator turn", spoken.Role)
	}
	if !strings.Contains(spoken.Content, fieldLegalName) {
		t.Errorf("the spoken click does not name its field: %q", spoken.Content)
	}
	// The value is carried, and carried as DATA: an option's value is whatever
	// the crawled page said, so it belongs inside this call's declared boundary
	// rather than in the prompt's own voice.
	marker, ok := promptfence.MarkerIn(trace.Requests[0].System)
	if !ok {
		t.Fatal("the system prompt declares no boundary")
	}
	if !strings.Contains(spoken.Content, "<"+marker+">Acme Robotics GmbH</"+marker+">") {
		t.Errorf("the clicked value is not inside this call's boundary: %q", spoken.Content)
	}
	if last := messages[len(messages)-1]; last.Content != fixture.Message {
		t.Errorf("the current message is not the last turn: %+v", last)
	}
}
