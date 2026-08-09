// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for growth_fit.
//
// It certifies the shipped path: the request is built by orgdossier's own
// writer and the reply is read by orgdossier's own parser and grounding
// filter. A case that rebuilt either would measure a copy, and a copy stays
// green through the change that breaks the original.
//
// What the expectation MEANS here is two things, because a growth fit is two
// claims. First the records a defensible assessment has to be grounded in —
// not the wording, since the prose is a judgment and pinning its sentences
// would fail a good assessment for choosing different words. Second the band
// itself, as a set of ACCEPTABLE bands rather than one: whether a company is a
// strong or a moderate fit is a judgment two careful readers can differ on,
// while calling a clear misfit strong is a failure at the only thing this
// surface is for.
//
// The fixture names its records by LABEL. Prepare mints the ids, so an id in
// the reply is an id the model was handed rather than one the corpus author
// could have written into the expected answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// growthFitFixture is one company as the assessment reads it.
type growthFitFixture struct {
	ProfileFields []growthFitFieldFixture `json:"profile_fields"`
	Facts         []growthFitFactFixture  `json:"facts"`
}

type growthFitFieldFixture struct {
	Label string `json:"label"`
	Field string `json:"field"`
	Value string `json:"value"`
}

type growthFitFactFixture struct {
	Label string `json:"label"`
	Field string `json:"field"`
	Value string `json:"value"`
}

// growthFitExpectation is what a correct assessment must do: cite these
// records, and land on one of these bands.
type growthFitExpectation struct {
	Cites []string `json:"cites"`
	Bands []string `json:"bands"`
}

type growthFitCases struct{}

func (growthFitCases) Site() aitasks.Site {
	return aitasks.Site{Task: ai.TaskGrowthFit, Variant: "growth_fit", Kind: ai.SiteKindOneShot}
}

// Prepare turns one company and the records a defensible assessment must rest
// on into a runnable case, minting an id per labelled record.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (growthFitCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f growthFitFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("growth_fit: the fixture is not the shape this site takes: %w", err)
	}
	var want growthFitExpectation
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("growth_fit: the expected answer is not a cites/bands pair: %w", err)
	}
	in, label, err := growthFitInput(f)
	if err != nil {
		return nil, fmt.Errorf("growth_fit: %w", err)
	}
	if err := refuseUngroundableFit(want, label); err != nil {
		return nil, fmt.Errorf("growth_fit: %w", err)
	}
	return &growthFitCase{
		in: in, label: label, expected: want,
	}, nil
}

// growthFitInput builds the production input, minting one id per labelled
// record so no id in the reply can have come from the corpus.
func growthFitInput(f growthFitFixture) (orgdossier.Input, map[string]string, error) {
	in := orgdossier.Input{OrganizationID: ids.NewV7().String()}
	label := map[string]string{}
	for _, field := range f.ProfileFields {
		if err := refuseUnnameable(field.Label, "profile field", label); err != nil {
			return in, nil, err
		}
		id := ids.NewV7()
		label[field.Label] = id.String()
		wireID := openapi_types.UUID(id)
		in.ProfileFields = append(in.ProfileFields, crmcontracts.CompanyProfileField{
			Id:    &wireID,
			Field: crmcontracts.CompanyProfileFieldField(field.Field),
			Value: field.Value,
		})
	}
	for _, fact := range f.Facts {
		if err := refuseUnnameable(fact.Label, "fact", label); err != nil {
			return in, nil, err
		}
		id := ids.NewV7()
		label[fact.Label] = id.String()
		wireID := openapi_types.UUID(id)
		in.Facts = append(in.Facts, crmcontracts.OrganizationFact{
			Id:    &wireID,
			Field: crmcontracts.OrganizationFactField(fact.Field),
			Value: fact.Value,
		})
	}
	return in, label, nil
}

// refuseUngroundableFit names an expectation no reply could satisfy, or one
// every reply satisfies. Either measures nothing for as long as it stays in
// the corpus, which is worse than having no scenario: it reads as coverage.
func refuseUngroundableFit(want growthFitExpectation, label map[string]string) error {
	if len(want.Cites) == 0 {
		return errors.New("the scenario expects no cited record, so no reply could disagree with it")
	}
	for _, name := range want.Cites {
		if _, ok := label[name]; !ok {
			return fmt.Errorf(
				"the scenario expects %q, which the fixture does not supply, so the reply could never cite it", name)
		}
	}
	if len(want.Bands) == 0 {
		return errors.New("the scenario accepts no band, so every reply fails it")
	}
	distinct := map[string]bool{}
	for _, band := range want.Bands {
		if !orgdossier.BandIsJudgeable(crmcontracts.GrowthFitBand(band)) {
			return fmt.Errorf(
				"the scenario accepts band %q, which is not a band the model may propose", band)
		}
		// Counted distinctly, so a list padded with repeats cannot slip past the
		// accepts-everything check below while still accepting everything.
		distinct[band] = true
	}
	if len(distinct) == orgdossier.JudgeableBandCount {
		return errors.New("the scenario accepts every band, so no reply could disagree with it")
	}
	return nil
}

// growthFitCase certifies one assessment of one company.
type growthFitCase struct {
	in       orgdossier.Input
	label    map[string]string
	expected growthFitExpectation
}

// Run issues the one request this site sends, through the production writer's
// own request builder.
func (c *growthFitCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := orgdossier.GrowthFitRequest(c.in)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("growth_fit: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate runs the production parser and grounding filter, then asks the two
// questions the scenario poses: is the band defensible, and did the claims
// behind it rest on the records that matter.
func (c *growthFitCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	band, kept, err := orgdossier.ParseGrowthFit(trace.Output, c.in)
	if err != nil {
		// A reply production would discard. Abstention and a malformed answer
		// are both "no assessment", and the parser draws that line already.
		return aitasks.Outcome{Result: aitasks.OutcomeAbstained, Detail: err.Error()}
	}
	if !c.bandAccepted(band) {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("band %q, want one of %s", band, strings.Join(c.expected.Bands, ", ")),
		}
	}
	cited := map[string]bool{}
	for _, sentence := range kept.All() {
		for _, evidence := range sentence.Evidence {
			cited[evidence.EntityID] = true
		}
	}
	var missing []string
	for _, name := range c.expected.Cites {
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

func (c *growthFitCase) bandAccepted(band crmcontracts.GrowthFitBand) bool {
	for _, want := range c.expected.Bands {
		if crmcontracts.GrowthFitBand(want) == band {
			return true
		}
	}
	return false
}
