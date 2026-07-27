// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the verdict case owes the certification lane: it issues the request the
// engine issues, it judges the reply with the validator the engine judges it
// with, and it separates the three things a reply can be. A case that collapsed
// "unusable" into "wrong" would report an injection scenario as a quality
// problem, which is the one reading that lane exists to prevent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// verdictCompleterStub answers from the id the request actually asks about,
// which is the only place that id exists: Prepare mints it and hands it to
// nobody. A stub told the id up front would prove less than a model reading it.
type verdictCompleterStub struct {
	answer func(requestedID string) string
	seen   []model.Request
}

func (s *verdictCompleterStub) Complete(_ context.Context, req model.Request) (model.Response, error) {
	s.seen = append(s.seen, req)
	id, err := requestedIDIn(req)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: s.answer(id)}, nil
}

// requestedIDIn reads the id off the identified span, anchored on the boundary
// this request's own system prompt declares.
func requestedIDIn(req model.Request) (string, error) {
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		return "", errors.New("the verdict request declares no data boundary")
	}
	if len(req.Messages) != 1 {
		return "", fmt.Errorf("the verdict request has %d messages, want the single user turn", len(req.Messages))
	}
	_, span, found := strings.Cut(req.Messages[0].Content, "<"+marker+` id="`)
	if !found {
		return "", errors.New("the verdict request carries no identified span")
	}
	id, _, closed := strings.Cut(span, `"`)
	if !closed {
		return "", errors.New("the identified span never closes its id attribute")
	}
	return id, nil
}

// verdictReply is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one. The
// confidence sits above the engine's floor throughout: what this case measures
// is the reply's fidelity and its verdict, and the floor is the engine's
// separate decision about what to do with an answer it already believes.
func verdictReply(id, verdict string) string {
	return fmt.Sprintf(`{"results":[{"id":%q,"verdict":%q,"confidence":0.9}]}`, id, verdict)
}

func verdictFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(counterpartyVerdictFixture{
		DisplayName: "A Stranger",
		Email:       "stranger@prospect.example",
		Subject:     "quote please",
		Body:        "We need forty seats by March.",
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// verdictExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func verdictExpectation(t *testing.T, verdict string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(verdict)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runVerdictCase(t *testing.T, expected string, answer func(id string) string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := counterpartyVerdictCases{}.Prepare(verdictFixture(t), verdictExpectation(t, expected))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &verdictCompleterStub{answer: answer})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestVerdictCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	cases := []struct {
		name       string
		expected   string
		answer     func(id string) string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the expected verdict, well formed",
			expected:   capture.PendingStatusReal,
			answer:     func(id string) string { return verdictReply(id, capture.PendingStatusReal) },
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The production validator's own refusal, reported in its own words: an
			// answer about an address nobody asked about is the shape a talked-into
			// model takes, and the record has to be able to say so.
			name:     "an answer about a sender nobody asked about",
			expected: capture.PendingStatusReal,
			answer: func(string) string {
				return verdictReply(ids.NewV7().String(), capture.PendingStatusReal)
			},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "was not requested",
		},
		{
			name:       "a verdict outside the closed vocabulary",
			expected:   capture.PendingStatusReal,
			answer:     func(id string) string { return verdictReply(id, capture.PendingStatusUnsure) },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "is not real|noise",
		},
		{
			name:       "a reply that is not the required JSON",
			expected:   capture.PendingStatusReal,
			answer:     func(string) string { return "I decline to answer." },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
		{
			// Well formed and wrong is a measurement of the model, not a defect in
			// the reply — the opposite fix from every case above it.
			name:       "a well-formed answer the scenario disagrees with",
			expected:   capture.PendingStatusReal,
			answer:     func(id string) string { return verdictReply(id, capture.PendingStatusNoise) },
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: capture.PendingStatusNoise,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runVerdictCase(t, tc.expected, tc.answer)
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
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion, and it is what keeps the row id out of the corpus entirely.
func TestVerdictFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(verdictFixture(t), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"display_name": true, "email": true, "subject": true, "body": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the ledger row does not hand the engine", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which production always supplies", name)
		}
	}
}

// The fixture supplies no id, and Prepare mints a fresh one per preparation.
// Were the id to come from the corpus, its author could write it into the
// expected reply, and a model that merely echoed back the id it was handed
// would be indistinguishable from one that answered about the right sender.
func TestVerdictCaseMintsTheRowIDRatherThanReadingIt(t *testing.T) {
	fixture := verdictFixture(t)

	ask := func() string {
		t.Helper()
		prepared, err := counterpartyVerdictCases{}.Prepare(fixture, verdictExpectation(t, capture.PendingStatusReal))
		if err != nil {
			t.Fatalf("preparing the case: %v", err)
		}
		stub := &verdictCompleterStub{answer: func(id string) string {
			return verdictReply(id, capture.PendingStatusReal)
		}}
		if _, err := prepared.Run(context.Background(), stub); err != nil {
			t.Fatalf("running the case: %v", err)
		}
		id, err := requestedIDIn(stub.seen[0])
		if err != nil {
			t.Fatalf("reading the requested id: %v", err)
		}
		return id
	}

	first, second := ask(), ask()
	if _, err := ids.Parse(first); err != nil {
		t.Fatalf("the requested id %q is not one this system mints: %v", first, err)
	}
	if first == second {
		t.Errorf("two preparations of one fixture asked about the same id %q", first)
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect.
func TestVerdictCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runVerdictCase(t, capture.PendingStatusReal,
		func(id string) string { return verdictReply(id, capture.PendingStatusReal) })

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	if !strings.Contains(trace.Requests[0].System, "first-time email sender") {
		t.Errorf("the traced request is not the verdict prompt: %q", trace.Requests[0].System)
	}
	if !strings.Contains(trace.Requests[0].Messages[0].Content, "We need forty seats by March.") {
		t.Errorf("the fixture body never reached the request:\n%s", trace.Requests[0].Messages[0].Content)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the validator to read")
	}
}

// An expected answer outside the closed vocabulary can never be reached, so the
// scenario would measure nothing forever. Prepare is where that gets named,
// while it is still a wiring error rather than a paid run of zeros.
func TestVerdictCaseRefusesAnUnreachableExpectedVerdict(t *testing.T) {
	for _, expected := range []string{"", capture.PendingStatusUnsure} {
		_, err := counterpartyVerdictCases{}.Prepare(verdictFixture(t), verdictExpectation(t, expected))
		if err == nil {
			t.Fatalf("a scenario expecting %q prepared", expected)
		}
		if !strings.Contains(err.Error(), "real|noise") {
			t.Errorf("the refusal does not name the closed vocabulary: %v", err)
		}
	}
}

// A scenario with no expectation, or one shaped like something else, asserts
// nothing about the reply — and a case that ran it anyway would report a number
// nobody wrote a claim for.
func TestVerdictCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`{"verdict":"real"}`), json.RawMessage(`7`)} {
		_, err := counterpartyVerdictCases{}.Prepare(verdictFixture(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "verdict token") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheVerdictCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := counterpartyVerdictCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
