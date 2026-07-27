// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the classify case owes the certification lane: it issues the request the
// engine issues, it judges the reply with the batch-fidelity validator the
// engine judges it with, and it separates the three things a reply can be. A
// batch reply breaks in ways a single answer cannot — a label for a message
// nobody sent, one message answered twice, one left out — and every one of them
// is unusable rather than wrong. A case that collapsed them into "wrong" would
// report a broken batch contract as a quality problem.

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

// classifyCompleterStub answers from the ids the request actually asks about,
// which is the only place those ids exist: Prepare mints them and hands them to
// nobody. A stub told the ids up front would prove less than a model reading
// them.
type classifyCompleterStub struct {
	answer func(requestedIDs []string) string
	seen   []model.Request
}

func (s *classifyCompleterStub) Complete(_ context.Context, req model.Request) (model.Response, error) {
	s.seen = append(s.seen, req)
	requested, err := requestedIDsIn(req)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: s.answer(requested)}, nil
}

// requestedIDsIn reads the ids off the identified spans, in prompt order,
// anchored on the boundary this request's own system prompt declares.
func requestedIDsIn(req model.Request) ([]string, error) {
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		return nil, errors.New("the classify request declares no data boundary")
	}
	if len(req.Messages) != 1 {
		return nil, fmt.Errorf("the classify request has %d messages, want the single user turn", len(req.Messages))
	}
	var out []string
	rest := req.Messages[0].Content
	for {
		_, span, found := strings.Cut(rest, "<"+marker+` source_id="`)
		if !found {
			break
		}
		id, after, closed := strings.Cut(span, `"`)
		if !closed {
			return nil, errors.New("an identified span never closes its id attribute")
		}
		out = append(out, id)
		rest = after
	}
	if len(out) == 0 {
		return nil, errors.New("the classify request carries no identified span")
	}
	return out, nil
}

// classifyReplyText is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
func classifyReplyText(results ...string) string {
	return `{"results":[` + strings.Join(results, ",") + `]}`
}

// classifyVerdictFor is one labeled message in the shape the classify prompt
// demands. The confidence sits above the engine's floor throughout: what this
// case measures is the reply's fidelity and its labels, and the floor is the
// engine's separate decision about what to do with an answer it already
// believes.
func classifyVerdictFor(id, label string) string {
	return fmt.Sprintf(`{"id":%q,"label":%q,"confidence":0.9}`, id, label)
}

// classifyAnswer answers every requested id, positionally, with the labels
// given — the shape of a reply that honoured the batch contract, whatever it
// says.
func classifyAnswer(labels ...string) func(requested []string) string {
	return func(requested []string) string {
		results := make([]string, 0, len(requested))
		for i, id := range requested {
			results = append(results, classifyVerdictFor(id, labels[i]))
		}
		return classifyReplyText(results...)
	}
}

// The two messages every case below labels: one asking for work, one arranging
// a time, so the expected labels differ and a case cannot pass by answering the
// same token twice.
func classifyFixtureJSON(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(captureClassifyFixture{
		{Subject: "quote please", Body: "We need forty seats by March."},
		{Subject: "lunch thursday", Body: "Shall we say noon at the usual place?"},
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// classifyExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func classifyExpectation(t *testing.T, labels ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(labels)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runClassifyCase(t *testing.T, expected []string, answer func(requested []string) string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := captureClassifyCases{}.Prepare(classifyFixtureJSON(t), classifyExpectation(t, expected...))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &classifyCompleterStub{answer: answer})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestClassifyCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	expected := []string{"commitment", "meeting"}

	cases := []struct {
		name       string
		answer     func(requested []string) string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the expected labels, well formed",
			answer:     classifyAnswer(expected...),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The production validator's own refusal, reported in its own words: a
			// label for a message this call never carried is the shape a talked-into
			// model takes, and the record has to be able to say so.
			name: "a label for a message nobody asked about",
			answer: func(requested []string) string {
				return classifyReplyText(classifyVerdictFor(ids.NewV7().String(), "noise"))
			},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "was not requested",
		},
		{
			name: "one message answered twice and the other not at all",
			answer: func(requested []string) string {
				return classifyReplyText(
					classifyVerdictFor(requested[0], "commitment"), classifyVerdictFor(requested[0], "noise"))
			},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "appears twice",
		},
		{
			// A silently dropped message would leave its row read out of the backlog
			// and never labeled.
			name: "a message left out of the results",
			answer: func(requested []string) string {
				return classifyReplyText(classifyVerdictFor(requested[0], "commitment"))
			},
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "missing from the results",
		},
		{
			name:       "a label outside the closed vocabulary",
			answer:     classifyAnswer("spam", "meeting"),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "is not commitment|meeting|noise",
		},
		{
			name:       "a reply that is not the required JSON",
			answer:     func([]string) string { return "I decline to answer." },
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
		{
			// Well formed and wrong is a measurement of the model, not a defect in
			// the reply — the opposite fix from every case above it.
			name:       "a well-formed answer the scenario disagrees with",
			answer:     classifyAnswer("commitment", "noise"),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `message 2 is labeled "noise" where the scenario expects "meeting"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runClassifyCase(t, expected, tc.answer)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// Every disagreement reaches the Detail, not just the first: a run that labeled
// one message right and two wrong is not the near miss the first line alone
// would read as.
func TestClassifyCaseNamesEveryMessageItDisagreesWith(t *testing.T) {
	outcome, _ := runClassifyCase(t, []string{"commitment", "meeting"}, classifyAnswer("noise", "noise"))

	if outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want a wrong answer", outcome.Result, outcome.Detail)
	}
	for _, want := range []string{"message 1", "message 2"} {
		if !strings.Contains(outcome.Detail, want) {
			t.Errorf("Detail = %q, want it to name %s", outcome.Detail, want)
		}
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion, and it is what keeps the minted ids out of the corpus entirely.
func TestClassifyFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(classifyFixtureJSON(t), &messages); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("the fixture carries no message")
	}
	given := map[string]bool{"subject": true, "body": true}
	for i, fields := range messages {
		for name := range fields {
			if !given[name] {
				t.Errorf("message %d carries %q, which the backlog row does not hand the engine", i+1, name)
			}
		}
		for name := range given {
			if _, present := fields[name]; !present {
				t.Errorf("message %d drops %q, which production always supplies", i+1, name)
			}
		}
	}
}

// The fixture supplies no id, and Prepare mints a fresh one per message per
// preparation. Were the ids to come from the corpus, their author could write
// them into the expected reply, and a model that merely echoed back ids it was
// handed would be indistinguishable from one that labeled the right messages.
func TestClassifyCaseMintsAnIDPerMessageRatherThanReadingIt(t *testing.T) {
	fixture := classifyFixtureJSON(t)

	ask := func() []string {
		t.Helper()
		prepared, err := captureClassifyCases{}.Prepare(fixture, classifyExpectation(t, "commitment", "meeting"))
		if err != nil {
			t.Fatalf("preparing the case: %v", err)
		}
		stub := &classifyCompleterStub{answer: classifyAnswer("commitment", "meeting")}
		if _, err := prepared.Run(context.Background(), stub); err != nil {
			t.Fatalf("running the case: %v", err)
		}
		requested, err := requestedIDsIn(stub.seen[0])
		if err != nil {
			t.Fatalf("reading the requested ids: %v", err)
		}
		return requested
	}

	first, second := ask(), ask()
	if len(first) != 2 {
		t.Fatalf("the request asks about %d messages, want one id per fixture entry", len(first))
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, first...), second...) {
		if _, err := ids.Parse(id); err != nil {
			t.Errorf("the requested id %q is not one this system mints: %v", id, err)
		}
		if seen[id] {
			t.Errorf("the id %q was asked about twice", id)
		}
		seen[id] = true
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect.
func TestClassifyCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runClassifyCase(t, []string{"commitment", "meeting"}, classifyAnswer("commitment", "meeting"))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	if !strings.Contains(trace.Requests[0].System, "You label captured emails") {
		t.Errorf("the traced request is not the classify prompt: %q", trace.Requests[0].System)
	}
	if !strings.Contains(trace.Requests[0].Messages[0].Content, "We need forty seats by March.") {
		t.Errorf("a fixture body never reached the request:\n%s", trace.Requests[0].Messages[0].Content)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the validator to read")
	}
}

// An expectation the validator can never satisfy would measure nothing for as
// long as it stayed in the corpus. Prepare is where that gets named, while it is
// still a wiring error rather than a paid run of zeros.
func TestClassifyCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name       string
		labels     []string
		wantReason string
	}{
		{
			name:       "a label outside the closed vocabulary",
			labels:     []string{"commitment", "spam"},
			wantReason: "commitment|meeting|noise",
		},
		{
			name:       "fewer labels than the batch carries messages",
			labels:     []string{"commitment"},
			wantReason: "one label per message",
		},
		{
			name:       "more labels than the batch carries messages",
			labels:     []string{"commitment", "meeting", "noise"},
			wantReason: "one label per message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := captureClassifyCases{}.Prepare(classifyFixtureJSON(t), classifyExpectation(t, tc.labels...))
			if err == nil {
				t.Fatalf("a scenario expecting %v prepared", tc.labels)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say why it is unreachable: %v", err)
			}
		})
	}
}

// A batch this site could never have been handed certifies a prompt the product
// cannot send: the backlog read caps a call at ten messages and truncates every
// body, so a fixture beyond either bound describes work no cycle does.
func TestClassifyCaseRefusesABatchProductionCouldNeverRead(t *testing.T) {
	oversized := make(captureClassifyFixture, classifyBatchSize+1)
	labels := make([]string, len(oversized))
	for i := range oversized {
		oversized[i] = captureClassifyMessage{Subject: "quote please", Body: "We need forty seats."}
		labels[i] = "commitment"
	}

	cases := []struct {
		name       string
		fixture    captureClassifyFixture
		labels     []string
		wantReason string
	}{
		{
			name:       "no message at all",
			fixture:    captureClassifyFixture{},
			labels:     []string{},
			wantReason: "supplies no message",
		},
		{
			name:       "more messages than one call ever carries",
			fixture:    oversized,
			labels:     labels,
			wantReason: "one call labels at most",
		},
		{
			name: "a body longer than the backlog read returns",
			fixture: captureClassifyFixture{
				{Subject: "quote please", Body: strings.Repeat("a", classifyBodyLimit+1)},
			},
			labels:     []string{"commitment"},
			wantReason: "truncates every body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := json.Marshal(tc.fixture)
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = captureClassifyCases{}.Prepare(fixture, classifyExpectation(t, tc.labels...))
			if err == nil {
				t.Fatal("a batch production could never read prepared")
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say what the backlog read would have handed it: %v", err)
			}
		})
	}
}

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestClassifyCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`{"labels":["noise"]}`), json.RawMessage(`7`)} {
		_, err := captureClassifyCases{}.Prepare(classifyFixtureJSON(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "list of labels") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// A fixture shaped like something else is not a batch, and running it would
// certify a prompt built from whatever survived the decode.
func TestClassifyCaseRefusesAFixtureItCannotRead(t *testing.T) {
	_, err := captureClassifyCases{}.Prepare(json.RawMessage(`{"subject":"quote please"}`), classifyExpectation(t, "noise"))
	if err == nil {
		t.Fatal("a fixture that is not a list of messages prepared")
	}
	if !strings.Contains(err.Error(), "shape this site takes") {
		t.Errorf("the refusal does not say what a fixture must be: %v", err)
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheClassifyCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := captureClassifyCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
