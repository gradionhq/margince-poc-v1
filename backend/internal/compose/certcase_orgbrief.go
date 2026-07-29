// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for summarize/org_brief.
//
// It certifies the shipped path: the request is built by orgbrief's own
// writer and the reply is read by orgbrief's own grounding filter, because
// that filter is the thing standing between a reader and a sentence about a
// record they cannot open. A case that rebuilt either would measure a copy,
// and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here: the records a correct brief has to be
// grounded in. Not the wording — a brief is prose, and pinning its sentences
// would fail a good brief for choosing different words. What production
// cannot guarantee, and this therefore measures, is whether the model wrote
// about the account it was given and cited it, rather than inventing a deal
// or a next step nobody can check.
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

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// orgBriefFixture is one account as the brief reads it.
type orgBriefFixture struct {
	Name     string                `json:"name"`
	Industry string                `json:"industry"`
	Strength int                   `json:"strength"`
	Contacts int                   `json:"contact_count"`
	Deals    []orgBriefDealFixture `json:"open_deals"`
	Recent   []orgBriefActFixture  `json:"recent"`
	// SectionsOmitted is what this reader was NOT allowed to see. Without it
	// no scenario could describe a restricted reader, so the per-viewer
	// guarantee — the brief must stay silent about a withheld section rather
	// than inferring around the gap — had no certification behind it.
	SectionsOmitted []string `json:"sections_omitted"`
}

type orgBriefDealFixture struct {
	Label       string `json:"label"`
	Name        string `json:"name"`
	Stage       string `json:"stage"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Stalled     bool   `json:"stalled"`
}

type orgBriefActFixture struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	At      string `json:"at"`
}

type orgBriefCases struct{}

func (orgBriefCases) Site() aitasks.Site {
	return aitasks.Site{Task: ai.TaskSummarize, Variant: "org_brief", Kind: ai.SiteKindOneShot}
}

// Prepare turns one account and the records a correct brief must be
// grounded in into a runnable case, minting an id per labelled record.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (orgBriefCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f orgBriefFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("summarize/org_brief: the fixture is not the shape this site takes: %w", err)
	}
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"summarize/org_brief: the expected answer is not a list of record labels the brief must cite: %w", err)
	}
	in, label, err := orgBriefInput(f)
	if err != nil {
		return nil, err
	}
	if err := refuseUngroundableBrief(want, label); err != nil {
		return nil, err
	}
	return &orgBriefCase{
		site: "summarize/org_brief", request: orgbrief.BriefRequest,
		in: in, orgID: ids.NewV7().String(), label: label, expected: want,
	}, nil
}

// orgBriefInput builds the production input, minting one id per labelled
// record so no id in the reply can have come from the corpus.
func orgBriefInput(f orgBriefFixture) (orgbrief.Input, map[string]string, error) {
	in := orgbrief.Input{
		Name: f.Name, Industry: f.Industry,
		Strength: f.Strength, ContactCount: f.Contacts,
		SectionsOmitted: f.SectionsOmitted,
	}
	// label maps a corpus label to the id minted for it, so Evaluate can ask
	// "did the brief cite the stalled deal" without the corpus ever naming
	// an id.
	label := map[string]string{}
	for _, deal := range f.Deals {
		if err := refuseUnnameable(deal.Label, "deal", label); err != nil {
			return in, nil, err
		}
		id := ids.NewV7().String()
		label[deal.Label] = id
		in.OpenDeals = append(in.OpenDeals, orgbrief.DealIn{
			ID: id, Name: deal.Name, Stage: deal.Stage,
			AmountMinor: deal.AmountMinor, Currency: deal.Currency, Stalled: deal.Stalled,
		})
	}
	for _, act := range f.Recent {
		if err := refuseUnnameable(act.Label, "activity", label); err != nil {
			return in, nil, err
		}
		id := ids.NewV7().String()
		label[act.Label] = id
		in.Recent = append(in.Recent, orgbrief.ActIn{
			ID: id, Kind: act.Kind, Subject: act.Subject, At: act.At,
		})
	}
	return in, label, nil
}

// refuseUnnameable rejects a label no expectation could refer to: a blank
// one names nothing, and a repeated one names two records, so an expectation
// using it means neither. Both would measure nothing for as long as they
// stayed in the corpus.
func refuseUnnameable(label, kind string, seen map[string]string) error {
	if strings.TrimSpace(label) == "" {
		return fmt.Errorf("a fixture %s carries no label, so no expectation could name it", kind)
	}
	if _, taken := seen[label]; taken {
		return fmt.Errorf("the fixture labels two records %q, so an expectation naming it means neither", label)
	}
	return nil
}

// refuseUngroundableBrief names an expectation no reply could satisfy. A
// label the fixture never supplied is unreachable, and an empty expectation
// is satisfied by every reply including one that says nothing — each would
// measure nothing for as long as it stayed in the corpus.
func refuseUngroundableBrief(want []string, label map[string]string) error {
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

// orgBriefCase certifies one piece of grounded prose about one account. Both
// summarize sites — the standing brief and the prepared questions — run it:
// they differ only in the request they send, and everything the case measures
// (the production grounding filter, the labelled records the reply must cite)
// is the same question asked of the same input.
type orgBriefCase struct {
	site     string
	request  func(orgbrief.Input) model.Request
	in       orgbrief.Input
	orgID    string
	label    map[string]string
	expected []string
}

// Run issues the one request this site sends, through the production
// writer's own request builder.
func (c *orgBriefCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := c.request(c.in)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("%s: %w", c.site, err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate runs the production grounding filter and asks whether the
// surviving sentences cite the records the scenario says a correct brief is
// about.
func (c *orgBriefCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	sentences, err := orgbrief.ParseBrief(trace.Output, c.orgID, c.in)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if len(sentences) == 0 {
		// Every sentence was dropped for citing nothing in the account: the
		// model wrote about something else, which production shows as no
		// prose at all.
		return aitasks.Outcome{
			Result: aitasks.OutcomeAbstained,
			Detail: "no sentence cited a record of this account",
		}
	}
	cited := map[string]bool{}
	for _, sentence := range sentences {
		for _, evidence := range sentence.Evidence {
			cited[evidence.EntityID] = true
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
