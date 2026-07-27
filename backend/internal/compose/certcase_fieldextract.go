// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for cold_start/field_extract.
//
// It certifies the shipped path rather than a description of it: the request
// comes from companyFactsRequest, the same builder extractFields calls, and the
// reply is judged by gateEvidence, the same no-guess gate extractFields applies.
// A case that rebuilt either would measure a copy, and a copy stays green
// through the change that breaks the original.
//
// What the expectation MEANS here: the fields that must survive the gate, with
// the values they must carry. It is a subset claim, never an inventory — a real
// page carries more facts than a scenario cares to pin, and demanding
// exhaustiveness would fail a read for being richer than its author imagined.
// The one thing this site can be wrong about is a fact: it either grounds the
// one the scenario named, or it grounds something else, or it grounds nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fieldExtractFixture is ONE source text in exactly the arguments its callers
// hand extractFields. AcceptedFields is the caller's own field vocabulary —
// cold start narrows the shared prompt to the contract's ColdStartField enum,
// and a fixture that could not say so would certify a gate nobody runs.
type fieldExtractFixture struct {
	SourceLabel    string   `json:"source_label"`
	SourceText     string   `json:"source_text"`
	SourceURL      string   `json:"source_url"`
	AcceptedFields []string `json:"accepted_fields"`
}

// fieldExtractCases serves the one site that reads company facts off a page.
type fieldExtractCases struct{}

func (fieldExtractCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskColdStart,
		Variant: "field_extract",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one source text and the facts the scenario expects from it into
// a runnable case, bounding the text exactly as extractFields does so the gate
// this case runs reads what the model this case calls was shown.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (fieldExtractCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f fieldExtractFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("cold_start/field_extract: the fixture is not the shape this site takes: %w", err)
	}
	var want map[string]string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("cold_start/field_extract: the expected answer is not a field to value map: %w", err)
	}
	if len(want) == 0 {
		return nil, errors.New(
			"cold_start/field_extract: the scenario expects no field, so no reply could disagree with it")
	}
	accepted := make(map[string]bool, len(f.AcceptedFields))
	for _, name := range f.AcceptedFields {
		accepted[name] = true
	}
	if err := refuseUnextractableExpectation(want, accepted); err != nil {
		return nil, err
	}
	return &fieldExtractCase{
		sourceLabel: f.SourceLabel,
		sourceText:  boundedExtractionText(f.SourceText),
		sourceURL:   f.SourceURL,
		accept:      func(name string) bool { return accepted[name] },
		expected:    want,
	}, nil
}

// refuseUnextractableExpectation names an expectation the gate can never
// satisfy: a field the prompt never offers is one no model was told exists, a
// field outside the fixture's own vocabulary is dropped as unknown on every
// reply, and an empty value is dropped as empty on every reply. Each would
// measure nothing for as long as it stayed in the corpus. Naming it here costs a
// parse; finding it later costs a paid run.
//
// Sorted so a fixture with two offences names the same one every time.
func refuseUnextractableExpectation(want map[string]string, accepted map[string]bool) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		switch {
		case !slices.Contains(extractionFieldNames, name):
			return fmt.Errorf(
				"cold_start/field_extract: the scenario expects %q, which this prompt never offers the model", name)
		case !accepted[name]:
			return fmt.Errorf(
				"cold_start/field_extract: the scenario expects %q, which the fixture's accepted_fields excludes", name)
		case strings.TrimSpace(want[name]) == "":
			return fmt.Errorf(
				"cold_start/field_extract: the scenario expects an empty value for %q, which the gate drops", name)
		}
	}
	return nil
}

// fieldExtractCase is one source text ready to be read, closed over the bounded
// text, the caller's field vocabulary, and the facts the scenario expects.
type fieldExtractCase struct {
	sourceLabel string
	sourceText  string
	sourceURL   string
	accept      func(string) bool
	expected    map[string]string
}

// Run issues the one request this site sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told to try
// again rather than the answer it gives.
func (c *fieldExtractCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := companyFactsRequest(c.sourceLabel, c.sourceText, c.sourceURL)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("cold_start/field_extract: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate runs the no-guess gate and only then asks whether what survived is
// what the scenario expects. The order is the meaning: a fact the gate refused
// is not a fact to disagree with.
//
// Nothing surviving is OutcomeInvalid and NOT an abstention, alone among the
// grounding sites. Everywhere else a reply that grounds nothing is a completed
// piece of work — the deep read carries on, the enrichment pass picks the person
// up next cycle — but here extractGrounded turns an empty gate result into the
// unreadable-source error a human is shown, so producing nothing IS this path's
// failure and must not wear the word for a right answer.
//
// A reply that grounded something is usable whatever else it claimed, so a
// missing or fabricated expected fact is a wrong answer, named as such.
func (c *fieldExtractCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	fields, dropped := gateEvidence(trace.Output, c.sourceText, c.sourceURL, c.accept)
	// Every refusal reaches the Detail whatever the result: a reply that grounded
	// the expected facts while fabricating evidence for three others is not the
	// clean run it would otherwise look like.
	detail := gateRefusals(dropped)
	if len(fields) == 0 {
		if len(detail) == 0 {
			detail = []string{"the model claimed no field at all"}
		}
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	if disagreements := expectationDisagreements(c.expected, groundedValues(fields)); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}
