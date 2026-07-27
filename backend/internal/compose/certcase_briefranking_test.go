// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the brief-ranking case owes the certification lane: it issues the request
// the ranker issues, it reads and bounds the reply the way the ranker does, and
// it separates the three things a reply can be. The separation carries the whole
// meaning of a run here, because the bounding REPAIRS a bad reply instead of
// refusing it — a case that graded the repaired queue would report a model that
// named nothing as a model that agreed with the deterministic order.
//
// That the ranker itself runs these same three functions is proven where the
// ranker lives (briefs.TestReorderRunsTheExportedRankPath): briefL2Ranker is not
// constructible from this package, and a re-creation of it here would be the
// copy this case exists to avoid.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The two deals every case below ranks: the deterministic leader, and the one
// the overnight reply argues for. The L2 pass exists to promote the second, so
// it is the one the scenarios expect to lead.
const (
	steadyForecast = "steady_forecast"
	overnightReply = "overnight_reply"
)

// briefRankCompleterStub answers from the ids the request actually carries,
// which is the only place they exist: Prepare mints them and hands them to
// nobody. A stub told the ids up front would prove less than a model reading
// them out of the prompt.
type briefRankCompleterStub struct {
	answer func(ordered []string) string
	seen   []model.Request
}

func (s *briefRankCompleterStub) Complete(_ context.Context, req model.Request) (model.Response, error) {
	s.seen = append(s.seen, req)
	ordered, err := rankedIDsIn(req)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: s.answer(ordered)}, nil
}

// rankedIDsIn reads the candidate ids off the request in the order it lists
// them, the way a model reading the prompt would.
func rankedIDsIn(req model.Request) ([]string, error) {
	if len(req.Messages) != 1 {
		return nil, fmt.Errorf("the re-order request has %d messages, want the single user turn", len(req.Messages))
	}
	var out []string
	for _, line := range strings.Split(req.Messages[0].Content, "\n") {
		rest, listed := strings.CutPrefix(line, "- ")
		if !listed {
			continue
		}
		id, _, delimited := strings.Cut(rest, ":")
		if !delimited {
			return nil, fmt.Errorf("the candidate line %q names no id", line)
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("the re-order request lists no candidate")
	}
	return out, nil
}

// briefRankReply is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
func briefRankReply(order ...string) string {
	quoted := make([]string, len(order))
	for i, id := range order {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return `{"order":[` + strings.Join(quoted, ",") + `]}`
}

func briefRankFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(briefRankingFixture{Candidates: []briefRankingCandidate{
		{
			Label: steadyForecast, Winnability: 0.8, Revenue: 0.75,
			Timing: 1, Momentum: 0.4, Warmth: 0.47, Composite: 0.825,
		},
		{
			Label: overnightReply, Winnability: 0.25, Revenue: 0.5,
			Timing: 0.7, Momentum: 1, Warmth: 0.6, Composite: 0.5525,
		},
	}})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// briefRankExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func briefRankExpectation(t *testing.T, leaders ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(leaders)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runBriefRankCase(t *testing.T, answer func(ordered []string) string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := briefRankingCases{}.Prepare(briefRankFixture(t), briefRankExpectation(t, overnightReply))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &briefRankCompleterStub{answer: answer})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestBriefRankingCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	cases := []struct {
		name       string
		answer     func(ordered []string) string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the expected leader, ordered whole",
			answer:     func(o []string) string { return briefRankReply(o[1], o[0]) },
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// Well formed and wrong is a measurement of the model's judgment, not
			// a defect in the reply — the opposite fix from every case below it.
			name:       "the deterministic order the L2 pass was asked to improve on",
			answer:     func(o []string) string { return briefRankReply(o[0], o[1]) },
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `position 1 ranks "steady_forecast" where the scenario expects "overnight_reply"`,
		},
		{
			// The bounding completes this into exactly the expected queue, which
			// is precisely why it must not read as an accepted ranking.
			name:       "a leader named and the rest left to the ranker",
			answer:     func(o []string) string { return briefRankReply(o[1]) },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "ordered 1 ids where the queue holds 2",
		},
		{
			name:       "a deal the model invented",
			answer:     func(o []string) string { return briefRankReply(ids.NewV7().String(), o[1]) },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "position 1 is not a candidate it was given",
		},
		{
			name:       "one deal ranked twice and the other never",
			answer:     func(o []string) string { return briefRankReply(o[1], o[1]) },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "position 2 is not a candidate it was given, or one it had already ranked",
		},
		{
			name:       "a reply that is not the required JSON",
			answer:     func([]string) string { return "I have re-ranked them for you." },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runBriefRankCase(t, tc.answer)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what keeps the deal ids out of the corpus
// entirely — see Prepare — and the label that replaces them is corpus-side
// vocabulary that must never reach the model.
func TestBriefRankingFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var decoded struct {
		Candidates []map[string]json.RawMessage `json:"candidates"`
	}
	if err := json.Unmarshal(briefRankFixture(t), &decoded); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(briefRankFixture(t), &top); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if len(top) != 1 {
		t.Errorf("the fixture carries %d top-level fields, want only the candidate queue", len(top))
	}
	given := map[string]bool{
		"label": true, "winnability": true, "revenue": true,
		"timing": true, "momentum": true, "warmth": true, "composite": true,
	}
	for _, candidate := range decoded.Candidates {
		for name := range candidate {
			if !given[name] {
				t.Errorf("a candidate carries %q, which the ranker is not handed or does not read", name)
			}
		}
		for name := range given {
			if _, present := candidate[name]; !present {
				t.Errorf("a candidate drops %q, which the ranker always renders", name)
			}
		}
	}
}

// The fixture supplies no id, and Prepare mints a fresh one per candidate per
// preparation. Were the ids to come from the corpus, their author could write
// them into the expected reply, and a model echoing back ids it was handed would
// be indistinguishable from one that ranked the right deals.
//
// The labels that stand in for them never reach the model, which is the same
// invariant from the other side: this prompt renders ids and numbers only, and a
// corpus-authored word appearing in it would be untrusted text on a site that
// declares no data boundary.
func TestBriefRankingCaseMintsDealIDsAndSendsNoLabel(t *testing.T) {
	fixture := briefRankFixture(t)

	ask := func() []string {
		t.Helper()
		prepared, err := briefRankingCases{}.Prepare(fixture, briefRankExpectation(t, overnightReply))
		if err != nil {
			t.Fatalf("preparing the case: %v", err)
		}
		stub := &briefRankCompleterStub{answer: func(o []string) string { return briefRankReply(o...) }}
		if _, err := prepared.Run(context.Background(), stub); err != nil {
			t.Fatalf("running the case: %v", err)
		}
		req := stub.seen[0]
		if marker, declared := promptfence.MarkerIn(req.System); declared {
			t.Errorf("the re-order prompt declares the data boundary %q, so something untrusted now reaches it", marker)
		}
		for _, label := range []string{steadyForecast, overnightReply} {
			if strings.Contains(req.System+req.Messages[0].Content, label) {
				t.Errorf("the corpus label %q reached the model, which is only ever shown ids and numbers", label)
			}
		}
		ordered, err := rankedIDsIn(req)
		if err != nil {
			t.Fatalf("reading the ranked ids: %v", err)
		}
		return ordered
	}

	first, second := ask(), ask()
	if len(first) != 2 {
		t.Fatalf("the request lists %d candidates, want the fixture's 2", len(first))
	}
	for _, id := range first {
		if _, err := ids.Parse(id); err != nil {
			t.Fatalf("the ranked id %q is not one this system mints: %v", id, err)
		}
	}
	if first[0] == second[0] {
		t.Errorf("two preparations of one fixture ranked the same id %q", first[0])
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect.
func TestBriefRankingCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runBriefRankCase(t, func(o []string) string { return briefRankReply(o[1], o[0]) })

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	if !strings.Contains(trace.Requests[0].System, "morning-brief deal queue") {
		t.Errorf("the traced request is not the re-order prompt: %q", trace.Requests[0].System)
	}
	if !strings.Contains(trace.Requests[0].Messages[0].Content, "momentum=1.00") {
		t.Errorf("the fixture's factors never reached the request:\n%s", trace.Requests[0].Messages[0].Content)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the ranker to read")
	}
}

// A fixture the deterministic layer could never have produced describes a call
// this build does not make, and a queue the model is never shown certifies
// nothing at all.
func TestBriefRankingCaseRefusesAFixtureProductionNeverBuilds(t *testing.T) {
	candidate := func(label string, composite float64) briefRankingCandidate {
		return briefRankingCandidate{Label: label, Composite: composite}
	}
	cases := []struct {
		name       string
		candidates []briefRankingCandidate
		wantDetail string
	}{
		{
			name:       "a queue too short to re-order",
			candidates: []briefRankingCandidate{candidate(steadyForecast, 0.8)},
			wantDetail: "the model is never called",
		},
		{
			name:       "a queue climbing out of the deterministic order",
			candidates: []briefRankingCandidate{candidate(steadyForecast, 0.4), candidate(overnightReply, 0.8)},
			wantDetail: "composite-descending order never hands the ranker",
		},
		{
			name:       "a candidate no expectation could name",
			candidates: []briefRankingCandidate{candidate(steadyForecast, 0.8), candidate("  ", 0.4)},
			wantDetail: "carries no label",
		},
		{
			name:       "two candidates wearing one label",
			candidates: []briefRankingCandidate{candidate(steadyForecast, 0.8), candidate(steadyForecast, 0.4)},
			wantDetail: "labels two candidates",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(briefRankingFixture{Candidates: tc.candidates})
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = briefRankingCases{}.Prepare(raw, briefRankExpectation(t, steadyForecast))
			if err == nil {
				t.Fatal("a fixture the deterministic layer never produces prepared")
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("the refusal does not say what is wrong with the fixture: %v", err)
			}
		})
	}
}

// An expected ranking the queue can never hold measures nothing for as long as
// it stays in the corpus, and one every reply satisfies measures just as little.
// Prepare is where both get named, while they are still wiring errors rather
// than a paid run of zeros.
func TestBriefRankingCaseRefusesAnUnreachableRanking(t *testing.T) {
	cases := []struct {
		name       string
		expected   json.RawMessage
		wantDetail string
	}{
		{
			name:       "no deal at all",
			expected:   briefRankExpectation(t),
			wantDetail: "no reply could disagree with it",
		},
		{
			name:       "more deals than the queue holds",
			expected:   briefRankExpectation(t, overnightReply, steadyForecast, "a_third"),
			wantDetail: "expects 3 ranked deals where the fixture supplies 2",
		},
		{
			name:       "a deal the fixture never supplied",
			expected:   briefRankExpectation(t, "a_deal_nobody_scored"),
			wantDetail: "which the fixture does not supply",
		},
		{
			name:       "one deal expected in two places",
			expected:   briefRankExpectation(t, overnightReply, overnightReply),
			wantDetail: "ranks every candidate exactly once",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := briefRankingCases{}.Prepare(briefRankFixture(t), tc.expected)
			if err == nil {
				t.Fatal("an unreachable ranking prepared")
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("the refusal does not say why the ranking is unreachable: %v", err)
			}
		})
	}
}

// A scenario with no expectation, or one shaped like something else, asserts
// nothing about the reply — and a case that ran it anyway would report a number
// nobody wrote a claim for.
func TestBriefRankingCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`{"order":["a"]}`), json.RawMessage(`7`)} {
		_, err := briefRankingCases{}.Prepare(briefRankFixture(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "list of candidate labels") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheBriefRankingCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := briefRankingCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
