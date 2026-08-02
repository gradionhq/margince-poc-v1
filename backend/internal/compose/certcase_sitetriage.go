// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for site_triage/triage.
//
// It certifies the shipped path, not a description of it: the request comes
// from triageRequest and the reply is read by gateTriageVerdict, the same two
// the worker uses. A case that rebuilt either would measure a copy, and a copy
// stays green through the change that breaks the original.
//
// What a run measures here is asymmetric, and the scenarios have to be read that
// way. A wrong `company` answer costs one junk record, which is visible and
// deletable. A wrong `personal` or `provider` answer costs a real customer their
// organization, silently. So the corpus weighs a false refusal far more heavily
// than a false company, and `unclear` is a correct answer whenever the page does
// not actually say — the worker reads the whole site in that case and the
// deterministic evidence decides.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// siteTriageFixture is one landing page, in the two fields the crawl hands the
// classifier.
type siteTriageFixture struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

// siteTriageCases serves the one site that classifies a landing page.
type siteTriageCases struct{}

func (siteTriageCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskSiteTriage,
		Variant: "triage",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one landing page and the class the scenario expects into a
// runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (siteTriageCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f siteTriageFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("site_triage/triage: the fixture is not the shape this site takes: %w", err)
	}
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("site_triage/triage: the expected answer is not a class token: %w", err)
	}
	// An expectation outside the closed vocabulary is unreachable: the gate
	// rewrites every reply that could satisfy it to `unclear`, so the scenario
	// would measure nothing for as long as it stayed in the corpus. Naming it
	// here costs a parse; finding it later costs a paid run.
	known := false
	for _, kind := range siteTriageKinds {
		known = known || kind == want
	}
	if !known {
		return nil, fmt.Errorf("site_triage/triage: the scenario expects %q, which is not one of %v", want, siteTriageKinds)
	}
	return &siteTriageCase{
		page:     crawlPage{URL: f.URL, Text: f.Text},
		expected: want,
	}, nil
}

// siteTriageCase is one fixture landing page ready to be classified.
type siteTriageCase struct {
	page     crawlPage
	expected string
}

// Run issues the one request this site sends.
func (c *siteTriageCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := triageRequest(c.page)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("site_triage/triage: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the worker's own gate first, then asks whether the answer is
// the one the scenario expects. The order is the meaning: a reply the gate had
// to rewrite never produced a class to disagree with.
//
// The gate turns every unusable reply into `unclear`, so a scenario expecting
// `unclear` cannot distinguish "the model read the page and said it does not
// say" from "the model produced garbage". Those are only the same OUTCOME, and
// the detail below records which one happened so a paid run is still readable.
func (c *siteTriageCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var raw siteTriageVerdict
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &raw); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	gated := gateTriageVerdict(trace.Output)
	// A confidence outside 0..1 is malformed whatever the kind says. Checking
	// only the kind would let `{"kind":"unclear","confidence":9}` score as a
	// correct abstention on an `unclear` scenario, crediting the model for
	// output the gate had to rewrite.
	if raw.Confidence < 0 || raw.Confidence > 1 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("the reply's confidence %.2f is outside 0 to 1", float64(raw.Confidence)),
		}
	}
	if gated.Kind == siteKindUnclear && raw.Kind != siteKindUnclear {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("the gate refused the reply and read it as unclear: %s", gated.Reason),
		}
	}
	if gated.Kind != c.expected {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the model answered %q at confidence %.2f where the scenario expects %q",
				gated.Kind, float64(gated.Confidence), c.expected),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}
