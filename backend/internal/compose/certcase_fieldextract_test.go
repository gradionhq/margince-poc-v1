// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the field-extraction case owes the certification lane: it issues the
// request the extractor issues, it judges the reply with the no-guess gate the
// extractor judges it with, and it separates a reply nothing survived from one
// that survived and says the wrong thing. The two want opposite fixes — a
// fabricating model is a prompt problem, a confidently-wrong one is a model
// choice — and a case that collapsed them could report neither.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fieldExtractPage is the source every case below reads. It carries the two
// facts the expectations quote, verbatim, because the gate demands the evidence
// be on the page and this test is not the place to fight it.
const fieldExtractPage = "Acme Robotics GmbH builds warehouse robots in Stuttgart. " +
	"We help RevOps leaders cut picking time in half. " +
	"Impressum: Acme Robotics GmbH, Königstraße 1, 70173 Stuttgart."

// fieldExtractCompleterStub answers with the canned reply a run is about. What
// the model was asked reaches the assertions through the trace, which is where
// the record and the canary gate read it from too.
type fieldExtractCompleterStub struct {
	reply string
}

func (s *fieldExtractCompleterStub) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// fieldExtractReply is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
func fieldExtractReply(claims ...string) string {
	return `{"fields":[` + strings.Join(claims, ",") + `]}`
}

// fieldExtractClaim is one claimed fact in the shape the extraction prompt
// demands. The confidence sits inside the gate's (0,1] range throughout, so a
// case that measures grounding is never failing on a number instead.
func fieldExtractClaim(field, value, evidence string) string {
	return fmt.Sprintf(`{"field":%q,"value":%q,"evidence_snippet":%q,"confidence":0.9}`, field, value, evidence)
}

func fieldExtractFixtureJSON(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fieldExtractFixture{
		SourceLabel: "Page",
		SourceText:  fieldExtractPage,
		SourceURL:   "https://acme.example",
		AcceptedFields: []string{
			string(crmcontracts.ColdStartFieldFieldLegalName),
			string(crmcontracts.ColdStartFieldFieldValueProposition),
		},
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// fieldExtractExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func fieldExtractExpectation(t *testing.T, want map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runFieldExtractCase(t *testing.T, want map[string]string, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := fieldExtractCases{}.Prepare(fieldExtractFixtureJSON(t), fieldExtractExpectation(t, want))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &fieldExtractCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

var fieldExtractWantLegalName = map[string]string{
	string(crmcontracts.ColdStartFieldFieldLegalName): "Acme Robotics GmbH",
}

func TestFieldExtractCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	legalNameClaim := fieldExtractClaim(
		string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH", "Impressum: Acme Robotics GmbH")

	cases := []struct {
		name       string
		want       map[string]string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the expected fact, grounded on the page",
			want:       fieldExtractWantLegalName,
			reply:      fieldExtractReply(legalNameClaim),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The gate's own refusal, reported in its own words. An invented quote
			// is the shape a fabricating model takes, and nothing survives it here,
			// which is precisely the unreadable answer production would give.
			name: "every claim quotes something the page never said",
			want: fieldExtractWantLegalName,
			reply: fieldExtractReply(fieldExtractClaim(
				string(crmcontracts.ColdStartFieldFieldLegalName), "Globex SE", "Globex SE, a subsidiary of Acme")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: dropEvidenceNotOnPage,
		},
		{
			name:       "a reply that is not the required JSON",
			want:       fieldExtractWantLegalName,
			reply:      "I could not read this page.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: dropUnparseableReply,
		},
		{
			name:       "a reply that claims nothing at all",
			want:       fieldExtractWantLegalName,
			reply:      fieldExtractReply(),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "claimed no field",
		},
		{
			// Grounded, usable, and not the fact the scenario pinned: a measurement
			// of the model, not a defect in the reply.
			name: "a grounded fact the scenario disagrees with",
			want: fieldExtractWantLegalName,
			reply: fieldExtractReply(fieldExtractClaim(
				string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics", "Acme Robotics GmbH builds warehouse robots")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "Acme Robotics GmbH",
		},
		{
			// A page can carry a fact the model simply never returns, and a run that
			// grounded something else is still a usable reply.
			name: "an expected fact the reply never mentions",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldLegalName):        "Acme Robotics GmbH",
				string(crmcontracts.ColdStartFieldFieldValueProposition): "cut picking time in half",
			},
			reply:      fieldExtractReply(legalNameClaim),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "no surviving " + string(crmcontracts.ColdStartFieldFieldValueProposition),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runFieldExtractCase(t, tc.want, tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A reply that grounds what the scenario asked for while fabricating something
// else is not the clean run it would otherwise look like, so every gate refusal
// reaches the Detail whatever the result. A record that hid them would report a
// fabricating model as a healthy one.
func TestFieldExtractCaseReportsGateDropsEvenWhenItAccepts(t *testing.T) {
	outcome, _ := runFieldExtractCase(t, fieldExtractWantLegalName, fieldExtractReply(
		fieldExtractClaim(
			string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH", "Impressum: Acme Robotics GmbH"),
		fieldExtractClaim(
			string(crmcontracts.ColdStartFieldFieldValueProposition), "we ship to 40 countries", "Acme ships to 40 countries"),
	))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, dropEvidenceNotOnPage) {
		t.Errorf("Detail = %q, want it to name the fabricated claim the gate dropped", outcome.Detail)
	}
}

// The gate forgives presentation and nothing else — the same relaxation it
// applies to evidence. A scenario that failed on a straightened apostrophe would
// measure typography; one that passed on a reworded value would measure nothing.
func TestFieldExtractCaseComparesValuesTheWayTheGateComparesEvidence(t *testing.T) {
	page := "Acme’s promise: cut picking time in half."
	fixture, err := json.Marshal(fieldExtractFixture{
		SourceLabel:    "Pasted company text",
		SourceText:     page,
		AcceptedFields: []string{string(crmcontracts.ColdStartFieldFieldValueProposition)},
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	prepared, err := fieldExtractCases{}.Prepare(fixture, fieldExtractExpectation(t, map[string]string{
		string(crmcontracts.ColdStartFieldFieldValueProposition): "Acme's promise",
	}))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &fieldExtractCompleterStub{
		reply: fieldExtractReply(fieldExtractClaim(
			string(crmcontracts.ColdStartFieldFieldValueProposition), "Acme’s  promise", "cut picking time in half")),
	})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	if outcome := prepared.Evaluate(trace); outcome.Result != aitasks.OutcomeAccepted {
		t.Errorf("Result = %q (%s), want a curly apostrophe to be the same value", outcome.Result, outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion.
func TestFieldExtractFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fieldExtractFixtureJSON(t), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"source_label": true, "source_text": true, "source_url": true, "accepted_fields": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which no caller of extractFields supplies", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which every extraction call carries", name)
		}
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect.
func TestFieldExtractCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runFieldExtractCase(t, fieldExtractWantLegalName, fieldExtractReply(fieldExtractClaim(
		string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH", "Impressum: Acme Robotics GmbH")))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	req := trace.Requests[0]
	if !strings.Contains(req.System, "You extract company facts") {
		t.Errorf("the traced request is not the extraction prompt: %q", req.System)
	}
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	content := req.Messages[0].Content
	openAt := strings.Index(content, "<"+marker+">"+fieldExtractPage+"</"+marker+">")
	if openAt < 0 {
		t.Errorf("the fixture page is not wrapped in the declared boundary:\n%s", content)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the gate to read")
	}
}

// An expectation the gate can never satisfy would measure nothing for as long as
// it stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestFieldExtractCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name       string
		want       map[string]string
		wantReason string
	}{
		{
			name:       "a field name the prompt never offers the model",
			want:       map[string]string{"favourite_colour": "blue"},
			wantReason: "never offers",
		},
		{
			name: "a field this fixture's own vocabulary rejects",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldIndustry): "robotics",
			},
			wantReason: "accepted_fields",
		},
		{
			name: "an empty value, which the gate drops from every reply",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldLegalName): "   ",
			},
			wantReason: "empty value",
		},
		{
			name:       "no expectation at all",
			want:       map[string]string{},
			wantReason: "expects no field",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fieldExtractCases{}.Prepare(fieldExtractFixtureJSON(t), fieldExtractExpectation(t, tc.want))
			if err == nil {
				t.Fatalf("a scenario expecting %v prepared", tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say why it is unreachable: %v", err)
			}
		})
	}
}

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestFieldExtractCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`["legal_name"]`), json.RawMessage(`7`)} {
		_, err := fieldExtractCases{}.Prepare(fieldExtractFixtureJSON(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "field to value") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheFieldExtractCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := fieldExtractCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
