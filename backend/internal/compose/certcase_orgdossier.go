// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for summarize/org_dossier.
//
// It certifies the shipped path: the request is built by orgdossier's own
// writer and the reply is read by orgdossier's own parser and grounding
// filter. A case that rebuilt either would measure a copy, and a copy stays
// green through the change that breaks the original.
//
// What the expectation MEANS here: the recorded fields a correct description
// has to be built on. Not the wording — the whole reason this lane exists is
// that the same facts read better as prose than as a list, and pinning
// sentences would fail a good dossier for choosing different words.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type orgDossierCases struct{}

func (orgDossierCases) Site() aitasks.Site {
	return aitasks.Site{Task: ai.TaskSummarize, Variant: "org_dossier", Kind: ai.SiteKindOneShot}
}

// Prepare reuses the growth-fit fixture shape: both sites read one company's
// recorded facts, and a second fixture format would be a second thing to keep
// true about the same records.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (orgDossierCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f growthFitFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("summarize/org_dossier: the fixture is not the shape this site takes: %w", err)
	}
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"summarize/org_dossier: the expected answer is not a list of record labels the dossier must cite: %w", err)
	}
	in, label, err := growthFitInput(f)
	if err != nil {
		return nil, fmt.Errorf("summarize/org_dossier: %w", err)
	}
	if err := refuseUngroundableDossier(want, label); err != nil {
		return nil, fmt.Errorf("summarize/org_dossier: %w", err)
	}
	return &orgDossierCase{in: in, label: label, expected: want}, nil
}

// refuseUngroundableDossier names an expectation no reply could satisfy, or one
// every reply satisfies. Either measures nothing for as long as it stays in the
// corpus, which is worse than no scenario: it reads as coverage.
func refuseUngroundableDossier(want []string, label map[string]string) error {
	if len(want) == 0 {
		return errors.New("the scenario expects no cited record, so no reply could disagree with it")
	}
	for _, name := range want {
		if _, ok := label[name]; !ok {
			return fmt.Errorf(
				"the scenario expects %q, which the fixture does not supply, so the reply could never cite it", name)
		}
	}
	return nil
}

type orgDossierCase struct {
	in       orgdossier.Input
	label    map[string]string
	expected []string
}

// Run issues the one request this site sends, through the production writer's
// own request builder.
func (c *orgDossierCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := orgdossier.DossierRequest(c.in)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("summarize/org_dossier: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate runs the production grounding filter and asks whether the surviving
// sentences describe the company they were given.
func (c *orgDossierCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	sections, err := orgdossier.ParseDossier(trace.Output, c.in)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	cited := map[string]bool{}
	for _, section := range sections {
		for _, sentence := range section.Sentences {
			for _, evidence := range sentence.Evidence {
				cited[evidence.EntityID] = true
			}
		}
	}
	if len(cited) == 0 {
		// Every sentence was dropped for citing nothing this company holds:
		// the model described something else, which production shows as the
		// deterministic floor instead.
		return aitasks.Outcome{
			Result: aitasks.OutcomeAbstained,
			Detail: "no sentence cited a record of this company",
		}
	}
	var missing []string
	for _, name := range c.expected {
		if !cited[c.label[name]] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: "never cited: " + strings.Join(missing, ", "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}
