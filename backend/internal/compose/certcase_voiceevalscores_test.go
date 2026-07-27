// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the judging case owes the certification lane: it issues the request the
// evaluation issues, it reads the answer with readVoiceJudgeScores — the
// evaluation's own reading — and it separates the three things an answer can be.
// A judge that returned nothing usable and a judge that ranked the wrong draft
// highest fail for opposite reasons: the first leaves half the candidate's
// signal missing, the second is the measurement this site exists to take.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The author sample every test below judges against, and the three drafts of it.
// The first is written in the sample's own rhythm, the second is generic AI
// prose, and the third is neither — which is what makes "draft 1 above draft 2"
// a claim about the judge rather than about the drafts' order in the list.
const (
	voiceJudgeAuthorSample = "Useful sentence about the work. We ship Monday. " +
		"The plan holds. I will send the numbers tonight."
	voiceJudgeNearDraft    = "Useful sentence about the work. We ship Monday and the plan holds."
	voiceJudgeGenericDraft = "Let us delve into the transformative synergies this ever-evolving partnership unlocks."
	voiceJudgeOtherDraft   = "Sounds good. Tuesday works."
)

// voiceJudgeDrafts is the repeat set one judging call carries. The evaluation
// judges one prompt's repeats together, so the count is the loop's, not a
// choice this test makes.
func voiceJudgeDrafts(t *testing.T) []string {
	t.Helper()
	drafts := []string{voiceJudgeNearDraft, voiceJudgeGenericDraft, voiceJudgeOtherDraft}
	if len(drafts) != voiceEvalRepeatsPerPrompt {
		t.Fatalf("the evaluation judges %d repeats per prompt, and these tests supply %d",
			voiceEvalRepeatsPerPrompt, len(drafts))
	}
	return drafts
}

func voiceEvalScoresFixtureOf(t *testing.T, sample string, drafts []string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(voiceEvalScoresFixture{AuthorSample: sample, Drafts: drafts})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// voiceEvalScoresRanking is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func voiceEvalScoresRanking(t *testing.T, order ...int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(order)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

// voiceJudgeReply is one judge answer, built as text rather than marshalled so a
// malformed answer is as expressible as a well-formed one.
func voiceJudgeReply(scores ...string) string {
	return `{"scores":[` + strings.Join(scores, ",") + `]}`
}

func runVoiceEvalScoresCase(t *testing.T, expected json.RawMessage, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := voiceEvalScoresCases{}.Prepare(
		voiceEvalScoresFixtureOf(t, voiceJudgeAuthorSample, voiceJudgeDrafts(t)), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &replyBrainStub{response: model.Response{Text: reply}})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestVoiceEvalScoresCaseSeparatesTheThreeThingsAnAnswerCanBe(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:  "the author's own rhythm ranked above generic prose",
			reply: voiceJudgeReply("0.9", "0.2", "0.5"), wantResult: aitasks.OutcomeAccepted,
			wantDetail: "0.9000",
		},
		{
			// The prompt asks for numbers in [0,1] and the reading clamps them,
			// so an out-of-range score still ranks rather than voiding the call.
			name:  "a score above the range the prompt asked for",
			reply: voiceJudgeReply("1.4", "0.2", "0.5"), wantResult: aitasks.OutcomeAccepted,
			wantDetail: "1.0000",
		},
		{
			name:  "generic prose ranked above the author's own rhythm",
			reply: voiceJudgeReply("0.2", "0.9", "0.5"), wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "the scenario expects draft 1 above draft 2",
		},
		{
			// A tie is not the ranking the scenario claims: the site exists to
			// tell one draft from another, and "cannot tell" is a wrong answer.
			name:  "the two drafts scored the same",
			reply: voiceJudgeReply("0.5", "0.5", "0.5"), wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "the scenario expects draft 1 above draft 2",
		},
		{
			name:  "fewer scores than drafts judged",
			reply: voiceJudgeReply("0.9", "0.2"), wantResult: aitasks.OutcomeInvalid,
			wantDetail: "scored 2 drafts, and 3 were judged",
		},
		{
			name:  "an answer that is not the required JSON",
			reply: "The first one reads more like the author.", wantResult: aitasks.OutcomeInvalid,
			wantDetail: `is not {"scores":[...]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runVoiceEvalScoresCase(t, voiceEvalScoresRanking(t, 1, 2), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// The ranking is a SUBSET claim over a chain, and every break in that chain
// reaches the Detail: an answer that got one comparison right and two wrong is
// not the near miss one line would read as.
func TestVoiceEvalScoresCaseNamesEveryBrokenComparison(t *testing.T) {
	outcome, _ := runVoiceEvalScoresCase(t, voiceEvalScoresRanking(t, 1, 3, 2), voiceJudgeReply("0.1", "0.9", "0.2"))

	if outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want a wrong answer", outcome.Result, outcome.Detail)
	}
	for _, want := range []string{"draft 1 above draft 3", "draft 3 above draft 2"} {
		if !strings.Contains(outcome.Detail, want) {
			t.Errorf("Detail = %q, want it to name %q", outcome.Detail, want)
		}
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's free
// text — the canary sweep does exactly that — without rewriting an assertion.
func TestVoiceEvalScoresFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	raw := voiceEvalScoresFixtureOf(t, voiceJudgeAuthorSample, voiceJudgeDrafts(t))
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"author_sample": true, "drafts": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the evaluation does not hand the judging call", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the evaluation always supplies", name)
		}
	}
}

// An expectation no answer could satisfy would measure nothing for as long as it
// stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestVoiceEvalScoresCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		drafts   []string
		expected json.RawMessage
		wantMsg  string
	}{
		{name: "an expectation shaped like something else", expected: json.RawMessage(`{"best":1}`), wantMsg: "not a ranking"},
		{name: "no expectation at all", expected: nil, wantMsg: "not a ranking"},
		{
			name:     "one draft named, so nothing is compared",
			expected: voiceEvalScoresRanking(t, 1), wantMsg: "compares nothing",
		},
		{
			name: "a draft named twice", expected: voiceEvalScoresRanking(t, 1, 2, 1),
			wantMsg: "names draft 1 twice",
		},
		{
			name: "a draft the call never carried", expected: voiceEvalScoresRanking(t, 1, 4),
			wantMsg: "names draft 4",
		},
		{
			// The judge is asked about drafts 1..n; a 0 is not one of them.
			name: "a draft numbered from zero", expected: voiceEvalScoresRanking(t, 0, 1),
			wantMsg: "names draft 0",
		},
		{
			// An unusable draft is sent as an empty body and its score is
			// discarded by the caller, so a ranking over one is never read.
			name:     "a draft the drafting half could not produce",
			drafts:   []string{voiceJudgeNearDraft, voiceJudgeGenericDraft, ""},
			expected: voiceEvalScoresRanking(t, 1, 3),
			wantMsg:  "draft 3 is empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drafts := tc.drafts
			if drafts == nil {
				drafts = voiceJudgeDrafts(t)
			}
			_, err := voiceEvalScoresCases{}.Prepare(
				voiceEvalScoresFixtureOf(t, voiceJudgeAuthorSample, drafts), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// A fixture the evaluation could never have been handed would certify a call the
// product does not make: the judge always compares against a held-out sample,
// and it always judges one prompt's whole repeat set at once.
func TestVoiceEvalScoresCaseRefusesAFixtureTheEvaluationCouldNotRun(t *testing.T) {
	cases := []struct {
		name    string
		sample  string
		drafts  []string
		wantMsg string
	}{
		{
			name: "no author sample to compare against", sample: "  ",
			drafts: voiceJudgeDrafts(t), wantMsg: "no author sample",
		},
		{
			name: "fewer drafts than one prompt's repeats", sample: voiceJudgeAuthorSample,
			drafts: []string{voiceJudgeNearDraft, voiceJudgeGenericDraft}, wantMsg: "judges all",
		},
		{
			name: "more drafts than one prompt's repeats", sample: voiceJudgeAuthorSample,
			drafts:  []string{voiceJudgeNearDraft, voiceJudgeGenericDraft, voiceJudgeOtherDraft, voiceJudgeNearDraft},
			wantMsg: "judges all",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := voiceEvalScoresCases{}.Prepare(
				voiceEvalScoresFixtureOf(t, tc.sample, tc.drafts), voiceEvalScoresRanking(t, 1, 2))
			if err == nil {
				t.Fatal("a fixture the evaluation could not run prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is wrong: %v", err)
			}
		})
	}
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same sample and the same drafts,
// answered by the same model text, must produce the same request and the same
// scores in the evaluation as in the case — and the evaluation's own usable/not
// verdict must be the case's invalid/not verdict, because they are the same
// reading.
func TestVoiceEvalScoresCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantScores []float64
		wantUsable bool
		wantResult string
	}{
		{
			name: "a usable verdict", reply: voiceJudgeReply("0.9", "0.2", "0.5"),
			wantScores: []float64{0.9, 0.2, 0.5}, wantUsable: true, wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "a usable verdict that ranks the wrong draft first", reply: voiceJudgeReply("0.2", "0.9", "0.5"),
			wantScores: []float64{0.2, 0.9, 0.5}, wantUsable: true, wantResult: aitasks.OutcomeWrongAnswer,
		},
		{
			// The neutral fallback is what the evaluation carries forward, and
			// it is exactly why the answer must also be reported unusable.
			name: "nothing the evaluation can read", reply: "The first one.",
			wantScores: []float64{0.5, 0.5, 0.5}, wantUsable: false, wantResult: aitasks.OutcomeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drafts := voiceJudgeDrafts(t)
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			scores, usable, err := judgeVoiceDrafts(context.Background(), brain, voiceJudgeAuthorSample, drafts)
			if err != nil {
				t.Fatalf("the evaluation's judging call did not complete: %v", err)
			}

			outcome, trace := runVoiceEvalScoresCase(t, voiceEvalScoresRanking(t, 1, 2), tc.reply)

			if usable != tc.wantUsable {
				t.Errorf("the evaluation reports usable=%t, want %t", usable, tc.wantUsable)
			}
			if len(scores) != len(tc.wantScores) {
				t.Fatalf("the evaluation scored %v, want %v", scores, tc.wantScores)
			}
			for i, want := range tc.wantScores {
				if scores[i] != want {
					t.Errorf("the evaluation scored draft %d at %g, want %g", i+1, scores[i], want)
				}
			}
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if len(trace.Requests) != 1 {
				t.Fatalf("the trace carries %d requests, want the one call this site issues", len(trace.Requests))
			}
			assertSameCompanyReadRequest(t, brain.request, trace.Requests[0])
			if trace.Output != tc.reply {
				t.Errorf("the trace records %q, want the model's own answer", trace.Output)
			}
		})
	}
}

// The judged drafts reach the request in the order the fixture lists them, and
// the numbering the expectation speaks in is that order. A case that reordered
// them would make every scenario's ranking claim about a different draft than
// the one its author meant.
func TestVoiceEvalScoresCaseNumbersTheDraftsTheFixtureOrders(t *testing.T) {
	_, trace := runVoiceEvalScoresCase(t, voiceEvalScoresRanking(t, 1, 2), voiceJudgeReply("0.9", "0.2", "0.5"))

	content := trace.Requests[0].Messages[0].Content
	previous := strings.Index(content, voiceJudgeAuthorSample)
	if previous < 0 {
		t.Fatal("the request does not carry the author sample it judges against")
	}
	for i, draft := range voiceJudgeDrafts(t) {
		label := fmt.Sprintf("\nDraft %d:\n", i+1)
		at := strings.Index(content, label)
		if at < 0 {
			t.Fatalf("the request carries no %q", strings.TrimSpace(label))
		}
		if at < previous {
			t.Errorf("draft %d is presented before what precedes it", i+1)
		}
		body := strings.Index(content[at:], draft)
		if body < 0 {
			t.Errorf("draft %d is not presented under its own number", i+1)
		}
		previous = at
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheVoiceEvalScoresCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := voiceEvalScoresCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
