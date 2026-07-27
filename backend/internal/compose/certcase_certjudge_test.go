// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the grader's own case owes the certification lane: it issues the request
// the harness issues, it reads the verdict with the read the harness reads it
// with, and it separates the three things a verdict can be. A grader that said
// nothing readable and a grader that scored a bad answer well fail for opposite
// reasons: the first leaves the run unscored, the second is the measurement this
// site exists to take.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The grading call every test below runs: a rubric this codebase wrote, the
// question the candidate was asked, and an answer that plainly satisfies it —
// which is what makes "score it 70-100" a claim about the grader rather than
// about the answer.
const (
	certJudgeRubric = "Score higher for a concrete, on-topic answer naming the material; lower for a vague or off-topic one."
	certJudgeInput  = "Describe the heat exchanger in one sentence."
	certJudgeAnswer = "The heat exchanger is a stainless-steel plate unit rated for 40 kW."
)

func certJudgeFixtureOf(t *testing.T, rubric, input, output string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(certJudgeFixture{Rubric: rubric, ScenarioInput: input, CandidateOutput: output})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// certJudgeBandOf is what the corpus asserts, encoded as the corpus will carry
// it — beside the fixture, never inside it.
func certJudgeBandOf(t *testing.T, minScore, maxScore int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(certJudgeBand{Min: minScore, Max: maxScore})
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

// certJudgeReply is one grader answer, built as text rather than marshalled so a
// malformed verdict is as expressible as a well-formed one.
func certJudgeReply(score, reason string) string {
	return `{"score": ` + score + `, "reason": "` + reason + `"}`
}

func runCertJudgeCase(t *testing.T, expected json.RawMessage, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := certJudgeCases{}.Prepare(
		certJudgeFixtureOf(t, certJudgeRubric, certJudgeInput, certJudgeAnswer), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &replyBrainStub{response: model.Response{Text: reply}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestCertJudgeCaseSeparatesTheThreeThingsAVerdictCanBe(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "a score inside the band the scenario claims",
			reply:      certJudgeReply("88", "concrete and names the material"),
			wantResult: aitasks.OutcomeAccepted, wantDetail: "scored 88",
		},
		{
			// The band is inclusive at both ends: a grader that lands exactly on
			// the boundary agreed with the scenario.
			name:       "a score on the band's own edge",
			reply:      certJudgeReply("70", "adequate"),
			wantResult: aitasks.OutcomeAccepted, wantDetail: "scored 70",
		},
		{
			name:       "a grader that failed a plainly good answer",
			reply:      certJudgeReply("20", "too short"),
			wantResult: aitasks.OutcomeWrongAnswer, wantDetail: "scored 20",
		},
		{
			// The verdict's reason is never graded — the harness keeps the score
			// and discards it, so a case must too.
			name:       "a score inside the band under a reason that says nothing",
			reply:      certJudgeReply("88", ""),
			wantResult: aitasks.OutcomeAccepted, wantDetail: "scored 88",
		},
		{
			name:       "a verdict that is not the required JSON",
			reply:      "I would give that about an 88 out of 100.",
			wantResult: aitasks.OutcomeInvalid, wantDetail: "not the expected JSON object",
		},
		{
			// A grader inventing its own scale is unusable, not a wrong answer:
			// there is no score to compare to a band.
			name:       "a score outside the scale the grader was given",
			reply:      certJudgeReply("880", "very good"),
			wantResult: aitasks.OutcomeInvalid, wantDetail: "outside 0-100",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runCertJudgeCase(t, certJudgeBandOf(t, 70, 100), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A run that disagreed must say what the grader actually said and what was
// expected of it — the two numbers a corpus author needs to tell a grader that
// is too harsh from a scenario whose band is too narrow.
func TestCertJudgeCaseNamesBothTheScoreAndTheBand(t *testing.T) {
	outcome, _ := runCertJudgeCase(t, certJudgeBandOf(t, 70, 100), certJudgeReply("20", "too short"))

	if outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want a wrong answer", outcome.Result, outcome.Detail)
	}
	for _, want := range []string{"scored 20", `"too short"`, "expects 70-100"} {
		if !strings.Contains(outcome.Detail, want) {
			t.Errorf("Detail = %q, want it to name %q", outcome.Detail, want)
		}
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's free
// text — the canary sweep does exactly that — without rewriting an assertion.
func TestCertJudgeFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	raw := certJudgeFixtureOf(t, certJudgeRubric, certJudgeInput, certJudgeAnswer)
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"rubric": true, "scenario_input": true, "candidate_output": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the harness does not hand the grader", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the harness always supplies", name)
		}
	}
}

// An expectation no verdict could ever be measured against would measure nothing
// for as long as it stayed in the corpus. Prepare is where that gets named, while
// it is still a wiring error rather than a paid run of zeros.
func TestCertJudgeCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		expected json.RawMessage
		wantMsg  string
	}{
		{name: "an expectation shaped like something else", expected: json.RawMessage(`[70, 100]`), wantMsg: "not a score band"},
		{name: "no expectation at all", expected: nil, wantMsg: "not a score band"},
		{name: "a band whose fields were never named", expected: json.RawMessage(`{"band": "high"}`), wantMsg: "ends at 0"},
		{name: "a band ending below where it starts", expected: certJudgeBandOf(t, 90, 40), wantMsg: "no score is inside"},
		{name: "a band starting below the scale", expected: certJudgeBandOf(t, -10, 40), wantMsg: "refused before it is ever compared"},
		{name: "a band reaching past the scale", expected: certJudgeBandOf(t, 70, 120), wantMsg: "refused before it is ever compared"},
		{name: "a band of one exact score", expected: certJudgeBandOf(t, 80, 80), wantMsg: "expects exactly 80"},
		{name: "a band covering the whole scale", expected: certJudgeBandOf(t, 0, 100), wantMsg: "no grader could disagree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := certJudgeCases{}.Prepare(
				certJudgeFixtureOf(t, certJudgeRubric, certJudgeInput, certJudgeAnswer), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// A grading call with nothing to grade against certifies a judgment the grader
// was never in a position to make.
func TestCertJudgeCaseRefusesACallItCouldNotGrade(t *testing.T) {
	cases := []struct {
		name          string
		rubric, input string
		wantMsg       string
	}{
		{name: "no rubric to score against", rubric: "  ", input: certJudgeInput, wantMsg: "no rubric"},
		{name: "no input the answer answers", rubric: certJudgeRubric, input: "", wantMsg: "no scenario input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := certJudgeCases{}.Prepare(
				certJudgeFixtureOf(t, tc.rubric, tc.input, certJudgeAnswer), certJudgeBandOf(t, 70, 100))
			if err == nil {
				t.Fatal("a call the grader could not have made prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is missing: %v", err)
			}
		})
	}
}

// A candidate that produced nothing is still graded in production — a reasoning
// model can spend its whole budget thinking and stop with no visible text — so a
// scenario asking whether the grader passes an empty answer must be runnable.
func TestCertJudgeCaseGradesAnEmptyCandidateOutput(t *testing.T) {
	prepared, err := certJudgeCases{}.Prepare(
		certJudgeFixtureOf(t, certJudgeRubric, certJudgeInput, ""), certJudgeBandOf(t, 0, 20))
	if err != nil {
		t.Fatalf("an empty candidate output is one production grades: %v", err)
	}
	trace, err := prepared.Run(context.Background(),
		&replyBrainStub{response: model.Response{Text: certJudgeReply("90", "excellent")}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	if outcome := prepared.Evaluate(trace); outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want a grader that passed nothing to be wrong", outcome.Result, outcome.Detail)
	}
}

// The claim this case makes is that it certifies the shipped path. The proof is
// that the request it issues IS the production builder's, argument for argument:
// the harness's own judge call builds it from the same three strings, so a case
// that reordered them — or built a request of its own — would certify a prompt
// the harness never sends.
func TestCertJudgeCaseIssuesTheProductionJudgeRequest(t *testing.T) {
	_, trace := runCertJudgeCase(t, certJudgeBandOf(t, 70, 100), certJudgeReply("88", "good"))

	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one call this site issues", len(trace.Requests))
	}
	want := JudgeRequest(certJudgeRubric, certJudgeInput, certJudgeAnswer)
	got := trace.Requests[0]
	if got.System != want.System {
		t.Errorf("the certified system prompt is not production's:\n%q\n%q", got.System, want.System)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("the certified request carries %d turns, production sends %d", len(got.Messages), len(want.Messages))
	}
	for i, message := range want.Messages {
		if got.Messages[i] != message {
			t.Errorf("certified turn %d = %+v, production sends %+v", i, got.Messages[i], message)
		}
	}
	if got.MaxTokens != want.MaxTokens {
		t.Errorf("the certified request caps the grader at %d, production caps it at %d", got.MaxTokens, want.MaxTokens)
	}
}

// The trace records the grader's own words, unaltered: the read Evaluate applies
// is the harness's, and it must see what the harness would have seen.
func TestCertJudgeCaseRecordsTheGradersOwnAnswer(t *testing.T) {
	reply := certJudgeReply("88", "concrete")
	_, trace := runCertJudgeCase(t, certJudgeBandOf(t, 70, 100), reply)

	if trace.Output != reply {
		t.Errorf("the trace records %q, want the grader's own answer", trace.Output)
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheCertJudgeCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := certJudgeCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
