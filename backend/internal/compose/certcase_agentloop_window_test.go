// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the two spellings owe a corpus author: the catalog word offers the surface
// this installation registers rather than a copy of it, an argument a scenario
// pins is compared as a subset claim, and every assertion neither spelling could
// ever satisfy is refused at Prepare instead of at a paid run.
//
// Each refusal here is paired with the case it exists to catch, because a guard
// that passes against the bug it describes is a guard nobody notices is gone.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The catalog tools these tests name. They are real registered verbs, so a
// rename reaches this file the same commit it reaches the surface — which is the
// property the catalog spelling exists to have.
const (
	agentLoopCatalogRead  = "search_records"
	agentLoopCatalogQuery = "q"
)

// agentLoopCatalogFixture is the base window said the other way: the same job,
// offered the surface the composition registers.
func agentLoopCatalogFixture() agentLoopFixture {
	fixture := agentLoopBaseFixture()
	fixture.Goal = "Find the Acme account."
	fixture.Tools = agentLoopToolWindow{catalog: true}
	return fixture
}

// runAgentLoopExpectation drives one case whose expectation is written out as
// JSON, which is how a corpus scenario carrying arguments reaches Prepare.
func runAgentLoopExpectation(
	t *testing.T, fixture agentLoopFixture, expected json.RawMessage, reply string,
) aitasks.Outcome {
	t.Helper()
	prepared, err := agentLoopCases{}.Prepare(agentLoopFixtureJSON(t, fixture), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &replyBrainStub{response: model.Response{Text: reply}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace)
}

// The point of the word: a scenario written against the catalog is offered the
// tools this installation actually registers. A hand-spelled copy would say the
// same thing today and keep saying it after a rename, which is the drift the
// corpus exists to catch rather than to have.
func TestAgentLoopCatalogWindowOffersTheRegisteredSurface(t *testing.T) {
	registered := NewRegistry(nil, SendPath{}).Specs()
	if len(registered) == 0 {
		t.Fatal("the composed registry offers no tools — this test checked nothing")
	}

	prepared, err := agentLoopCases{}.Prepare(
		agentLoopFixtureJSON(t, agentLoopCatalogFixture()),
		agentLoopExpectationJSON(t, agentLoopCatalogRead))
	if err != nil {
		t.Fatalf("preparing a catalog scenario: %v", err)
	}
	trace, err := prepared.Run(context.Background(),
		&replyBrainStub{response: model.Response{Text: `{"tool":"search_records","args":{"q":"Acme"}}`}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("got %d requests, want the single graded turn", len(trace.Requests))
	}

	// The window prints every offered tool, so the prompt is where "offered the
	// registered surface" is observable rather than asserted.
	system := trace.Requests[0].System
	for _, spec := range registered {
		if !strings.Contains(system, spec.Name) {
			t.Errorf("the catalog window never offers the registered tool %q", spec.Name)
		}
	}
	if outcome := prepared.Evaluate(trace); outcome.Result != aitasks.OutcomeAccepted {
		t.Errorf("Result = %q (%s), want the expected step to be reachable", outcome.Result, outcome.Detail)
	}
}

// A fixture naming a surface this site has no meaning for is a scenario about
// nothing, and it says so where the author is: at Prepare, by name.
func TestAgentLoopCaseRefusesAWindowSpellingItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools string
		want  string
	}{
		{
			name:  "a word this site does not know",
			tools: `"everything"`,
			want:  `the fixture offers the tool surface "everything"`,
		},
		{
			name:  "a surface that is neither a list nor a word",
			tools: `7`,
			want:  "neither a list of tools nor the word",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := agentLoopRawFixture(t, agentLoopBaseFixture(), tc.tools)
			_, err := agentLoopCases{}.Prepare(fixture, agentLoopExpectationJSON(t, agentLoopListTool))
			if err == nil {
				t.Fatal("Prepare accepted a window spelling this site cannot resolve")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal reads %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// agentLoopRawFixture re-writes the fixture's tool surface as literal JSON, so a
// test can hand this site a spelling the Go type could not hold.
func agentLoopRawFixture(t *testing.T, f agentLoopFixture, tools string) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(agentLoopFixtureJSON(t, f), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	fields["tools"] = json.RawMessage(tools)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// agentLoopArgExpectation writes the object spelling of an expected step.
func agentLoopArgExpectation(t *testing.T, step, args string) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"step":"` + step + `","args":` + args + `}`)
}

// The scenarios that pin arguments pin them as a SUBSET: every argument named
// has to be carried as named, and an argument the scenario never named is the
// call being richer than its author imagined rather than wrong.
func TestAgentLoopCaseGradesThePinnedArgumentsAndNoOthers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the call carries the pinned argument",
			args:       `{"q":"Acme"}`,
			reply:      `{"tool":"search_records","args":{"q":"Acme"}}`,
			wantResult: aitasks.OutcomeAccepted,
			wantDetail: `the turn took the step "search_records"`,
		},
		{
			name:       "the call carries more than the scenario pinned",
			args:       `{"q":"Acme"}`,
			reply:      `{"tool":"search_records","args":{"q":"Acme","record_type":"organization","limit":5}}`,
			wantResult: aitasks.OutcomeAccepted,
			wantDetail: `the turn took the step "search_records"`,
		},
		{
			name:       "the call omits the pinned argument",
			args:       `{"q":"Acme"}`,
			reply:      `{"tool":"search_records","args":{"record_type":"organization"}}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "q was not passed",
		},
		{
			name:       "the call passes the pinned argument as something else",
			args:       `{"q":"Acme"}`,
			reply:      `{"tool":"search_records","args":{"q":"the big account"}}`,
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `q was passed as "the big account", not "Acme"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := runAgentLoopExpectation(t, agentLoopCatalogFixture(),
				agentLoopArgExpectation(t, agentLoopCatalogRead, tc.args), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// Numbers are the one JSON value with two spellings of the same meaning and two
// meanings that share a spelling once rounded. A pinned limit is a real
// argument on this surface, so both halves are reachable from a scenario:
// float64 would grade the two integers below as equal, and comparing literals
// would grade 5 and 5.0 as different.
func TestAgentLoopCaseComparesPinnedNumbersExactly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       string
		reply      string
		wantResult string
	}{
		{
			name:       "the same number written two ways agrees",
			args:       `{"q":"Acme","limit":5}`,
			reply:      `{"tool":"search_records","args":{"q":"Acme","limit":5.0}}`,
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "two integers past float64's reach stay apart",
			args:       `{"q":"Acme","limit":9007199254740993}`,
			reply:      `{"tool":"search_records","args":{"q":"Acme","limit":9007199254740992}}`,
			wantResult: aitasks.OutcomeWrongAnswer,
		},
		{
			name:       "a number nested in a pinned object is compared too",
			args:       `{"q":"Acme","limit":{"page":9007199254740993}}`,
			reply:      `{"tool":"search_records","args":{"q":"Acme","limit":{"page":9007199254740992}}}`,
			wantResult: aitasks.OutcomeWrongAnswer,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := runAgentLoopExpectation(t, agentLoopCatalogFixture(),
				agentLoopArgExpectation(t, agentLoopCatalogRead, tc.args), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Errorf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
		})
	}
}

// A wrong step is a wrong step. Reporting what a call the scenario never wanted
// got wrong about its arguments would bury the one thing that went wrong under
// the details of a call that should not have been made.
func TestAgentLoopCaseReportsTheWrongStepWithoutItsArguments(t *testing.T) {
	outcome := runAgentLoopExpectation(t, agentLoopCatalogFixture(),
		agentLoopArgExpectation(t, agentLoopCatalogRead, `{"q":"Acme"}`),
		`{"tool":"read_record","args":{"record_type":"organization","id":"018f3a1b-0000-7000-8000-000000000010"}}`)

	if outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, aitasks.OutcomeWrongAnswer)
	}
	if !strings.Contains(outcome.Detail, `took the step "read_record" where the scenario expects "search_records"`) {
		t.Errorf("Detail = %q, want it to name the step disagreement", outcome.Detail)
	}
	if strings.Contains(outcome.Detail, agentLoopCatalogQuery+" was not passed") {
		t.Errorf("Detail = %q, want no argument disagreement under a wrong step", outcome.Detail)
	}
}

// An argument assertion the turn could never satisfy fails for the scenario's
// reason rather than the model's, and it would do so on every paid run until
// somebody read the record closely enough to notice.
func TestAgentLoopCaseRefusesAnArgumentNoCallCouldCarry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected json.RawMessage
		want     string
	}{
		{
			name:     "arguments pinned on the step that ends a run",
			expected: agentLoopArgExpectation(t, agentLoopFinalStep, `{"q":"Acme"}`),
			want:     "the step that ends a run carries none",
		},
		{
			name:     "an argument the tool's own schema does not declare",
			expected: agentLoopArgExpectation(t, agentLoopCatalogRead, `{"query":"Acme"}`),
			want:     `pins the argument "query" on search_records`,
		},
		{
			name:     "an expectation object carrying a key this site does not know",
			expected: json.RawMessage(`{"step":"search_records","arguments":{"q":"Acme"}}`),
			want:     "not a step name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentLoopCases{}.Prepare(agentLoopFixtureJSON(t, agentLoopCatalogFixture()), tc.expected)
			if err == nil {
				t.Fatal("Prepare accepted an argument assertion this site cannot measure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal reads %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// The fixture round trip: a scenario written in either spelling has to survive
// being encoded and read back, because that is what the corpus gates do to it
// before this site ever sees it.
func TestAgentLoopToolWindowSurvivesTheRoundTrip(t *testing.T) {
	for _, fixture := range []agentLoopFixture{agentLoopBaseFixture(), agentLoopCatalogFixture()} {
		var read agentLoopFixture
		if err := json.Unmarshal(agentLoopFixtureJSON(t, fixture), &read); err != nil {
			t.Fatalf("reading the fixture back: %v", err)
		}
		if read.Tools.catalog != fixture.Tools.catalog {
			t.Errorf("the window read back as catalog=%v, want %v", read.Tools.catalog, fixture.Tools.catalog)
		}
		if len(read.Tools.listed) != len(fixture.Tools.listed) {
			t.Errorf("the window read back with %d tools, want %d",
				len(read.Tools.listed), len(fixture.Tools.listed))
		}
	}
}
