// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for rate_extract/pricing.
//
// It certifies the shipped path rather than a description of it: the request
// comes from rateExtractRequest, the same builder the crawl calls, and the reply
// is judged by acceptRateRows, the same no-guess gate the crawl applies. A case
// that rebuilt either would measure a copy, and a copy stays green through the
// change that breaks the original.
//
// What the expectation MEANS here: the models the page grounds, each with the
// four price buckets it must carry. Prices are the whole product of this site —
// what reaches an administrator's approval queue is a per-model price — so a
// price is the one thing the model can be right or wrong about. It is a subset
// claim, never an inventory: a real pricing page states more models than a
// scenario cares to pin, and demanding exhaustiveness would fail a read for being
// richer than its author imagined.
//
// Two things the expectation deliberately cannot name. A PROVIDER: the gate
// overwrites every row's provider with the configured source, so naming one could
// only restate the fixture or assert something no reply can reach. A PASSAGE ID:
// the gate already requires that a row cite one, and which passage grounds a
// price is a fidelity question for the rubric — pinning an id in the corpus would
// couple every scenario to the fixture's own line numbering, where one blank line
// renumbers everything after it.

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

// ratePricingFixture is ONE crawled pricing page in exactly what the crawl hands
// the extraction: the fetched text, and the provider the installation configured
// this source under. The URL is deliberately absent — it selects which page is
// fetched and never reaches the model.
type ratePricingFixture struct {
	PageText string `json:"page_text"`
	Provider string `json:"provider"`
}

// ratePricedModel is one model's four price buckets, in the corpus's own words.
// All four are named because the prompt requires all four of every model, and a
// scenario that pinned only the input price would pass a reply that invented the
// other three.
type ratePricedModel struct {
	InputUsd      string `json:"input_per_mtok"`
	OutputUsd     string `json:"output_per_mtok"`
	CacheReadUsd  string `json:"cache_read_per_mtok"`
	CacheWriteUsd string `json:"cache_write_per_mtok"`
}

// asExtracted reads the scenario's prices in the shape the product's own
// converter takes, so both sides of a comparison go through allMicro.
func (p ratePricedModel) asExtracted() extractedModel {
	return extractedModel{
		InputUsd:      p.InputUsd,
		OutputUsd:     p.OutputUsd,
		CacheReadUsd:  p.CacheReadUsd,
		CacheWriteUsd: p.CacheWriteUsd,
	}
}

// priceLine renders four buckets for a human reading a disagreement.
func (p ratePricedModel) priceLine() string {
	return fmt.Sprintf("in %s, out %s, cache-read %s, cache-write %s",
		p.InputUsd, p.OutputUsd, p.CacheReadUsd, p.CacheWriteUsd)
}

// pricesOf reads a surviving row in the expectation's vocabulary, so a
// disagreement names both sides in the same words.
func pricesOf(em extractedModel) ratePricedModel {
	return ratePricedModel{
		InputUsd:      em.InputUsd,
		OutputUsd:     em.OutputUsd,
		CacheReadUsd:  em.CacheReadUsd,
		CacheWriteUsd: em.CacheWriteUsd,
	}
}

// ratePricingExpectation is one expected model, held twice: as the scenario
// wrote it, for a disagreement a human reads, and as the µUSD the sheet stores,
// which is the comparison the product itself makes. Converting once at Prepare
// is what lets Evaluate compare without a conversion that could fail there.
type ratePricingExpectation struct {
	stated  ratePricedModel
	buckets microBuckets
}

// ratePricingCases serves the one site that reads per-model AI prices off a
// vendor's pricing page.
type ratePricingCases struct{}

func (ratePricingCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskRateExtract,
		Variant: "pricing",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one pricing page and the prices the scenario expects from it into
// a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (ratePricingCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f ratePricingFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("rate_extract/pricing: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUncrawlablePage(f); err != nil {
		return nil, err
	}
	var stated map[string]ratePricedModel
	if err := json.Unmarshal(expected, &stated); err != nil {
		return nil, fmt.Errorf(
			"rate_extract/pricing: the expected answer is not a map of model id to its prices: %w", err)
	}
	if len(stated) == 0 {
		return nil, errors.New(
			"rate_extract/pricing: the scenario expects no model, so no reply could disagree with it")
	}
	want, err := convertExpectedPrices(stated)
	if err != nil {
		return nil, err
	}
	return &ratePricingCase{
		pageText: f.PageText,
		provider: strings.TrimSpace(f.Provider),
		expected: want,
	}, nil
}

// refuseUncrawlablePage names a fixture the crawl could never have produced.
// PricingSourcesFromMap skips a source with a blank provider, so a fixture
// without one describes a call this build does not make; and a page whose lines
// are all blank is numbered into nothing, leaving the model no passage to cite
// and therefore no row that could pass the gate.
func refuseUncrawlablePage(f ratePricingFixture) error {
	if strings.TrimSpace(f.Provider) == "" {
		return errors.New(
			"rate_extract/pricing: the fixture names no provider, and the crawl skips a source without one")
	}
	if numberPassages(f.PageText) == "" {
		return errors.New(
			"rate_extract/pricing: the fixture supplies no page text, so no passage could ground a price")
	}
	return nil
}

// convertExpectedPrices names an expectation the gate can never satisfy: a key
// that is not the model id a surviving row carries, and a price the sheet's own
// converter cannot read. Each would measure nothing for as long as it stayed in
// the corpus. Naming it here costs a parse; finding it later costs a paid run.
//
// Sorted so an expectation with two offences names the same one every time.
func convertExpectedPrices(stated map[string]ratePricedModel) (map[string]ratePricingExpectation, error) {
	want := make(map[string]ratePricingExpectation, len(stated))
	for _, id := range slices.Sorted(maps.Keys(stated)) {
		switch {
		case strings.TrimSpace(id) == "":
			return nil, errors.New("rate_extract/pricing: the scenario expects an entry that names no model")
		case strings.TrimSpace(id) != id:
			return nil, fmt.Errorf(
				"rate_extract/pricing: the scenario expects %q, and the gate trims every model id before it is compared",
				id)
		}
		buckets, convertible := allMicro(stated[id].asExtracted())
		if !convertible {
			return nil, fmt.Errorf(
				"rate_extract/pricing: the scenario expects %s for %q, which are not per-MTok decimals the sheet can store",
				stated[id].priceLine(), id)
		}
		want[id] = ratePricingExpectation{stated: stated[id], buckets: buckets}
	}
	return want, nil
}

// ratePricingCase is one pricing page ready to be read, closed over the
// configured provider the gate stamps on every surviving row and the prices the
// scenario expects.
type ratePricingCase struct {
	pageText string
	provider string
	expected map[string]ratePricingExpectation
}

// Run issues the one request this site sends, bare — and so does the refresh:
// the extraction goes over the plain completer seam, never asking for the
// shape-retry, so one page is one call there and here alike.
func (c *ratePricingCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := rateExtractRequest(c.pageText)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("rate_extract/pricing: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the crawl's own checks in the crawl's own order — parse, then
// the no-guess gate — and only then asks whether what survived is priced the way
// the scenario says the page prices it. The order is the meaning: a row the gate
// refused is not a price to disagree with.
//
// Nothing surviving is OutcomeInvalid and NOT an abstention. This site is not
// handed evidence that may or may not carry an answer: it is handed a pricing
// page a configured source publishes precisely to state prices, so a reply that
// prices nothing has failed to read the page rather than declined to invent one.
// A reply that grounded something is usable whatever else it claimed, so a
// missing or misread price is a wrong answer, named as such.
func (c *ratePricingCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	var out rateExtraction
	if err := json.Unmarshal([]byte(ai.Unfence(trace.Output)), &out); err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	kept := acceptRateRows(out.Models, c.provider)
	// Every refusal reaches the Detail whatever the result: a reply that priced
	// the expected models while inventing three ungrounded ones is not the clean
	// run it would otherwise look like.
	detail := rateRowRefusals(out.Models, c.provider)
	if len(kept) == 0 {
		if len(detail) == 0 {
			detail = []string{"the model priced nothing at all"}
		}
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	if disagreements := c.disagreements(kept); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}

// rateRowRefusals names every claimed row the gate did not let through, by asking
// the gate itself about each row one at a time. Asking rather than diffing the
// two slices keeps the answer exact when a reply prices one model twice, and
// keeps the reasons the gate's own rather than a second copy of them here.
func rateRowRefusals(claimed []extractedModel, provider string) []string {
	var out []string
	for i := range claimed {
		if len(acceptRateRows(claimed[i:i+1], provider)) == 1 {
			continue
		}
		if id := strings.TrimSpace(claimed[i].ModelID); id != "" {
			out = append(out, fmt.Sprintf("the gate refused the row for %q", id))
			continue
		}
		out = append(out, fmt.Sprintf("the gate refused row %d, which names no model", i+1))
	}
	return out
}

// disagreements names every expected model the surviving rows do not price the
// way the scenario says. All of them, not the first: a run that read one model
// right and two wrong is not the near miss one line would read as.
//
// Prices compare in the µUSD the sheet stores, which is the product's own
// comparison — so a scenario neither fails on a trailing zero nor passes on a
// rounding. A surviving row whose prices that converter cannot read is a
// disagreement rather than a refusal: the gate admitted it, and it is the sheet's
// diff that then drops it, leaving the scenario's price unstaged.
//
// Sorted so a run with two disagreements names them in the same order every time.
func (c *ratePricingCase) disagreements(kept []extractedModel) []string {
	// First wins, matching the sheet's own diff: it walks the extracted rows in
	// order, so a repeated model id stages from the first row that carried it.
	priced := make(map[string]extractedModel, len(kept))
	for i := range kept {
		if _, seen := priced[kept[i].ModelID]; !seen {
			priced[kept[i].ModelID] = kept[i]
		}
	}
	var out []string
	for _, id := range slices.Sorted(maps.Keys(c.expected)) {
		want := c.expected[id]
		got, grounded := priced[id]
		if !grounded {
			out = append(out, fmt.Sprintf("no surviving row for %q, which the scenario expects", id))
			continue
		}
		buckets, convertible := allMicro(got)
		switch {
		case !convertible:
			out = append(out, fmt.Sprintf("%q carries prices the sheet cannot read: %s", id, pricesOf(got).priceLine()))
		case buckets != want.buckets:
			out = append(out, fmt.Sprintf("%q is priced %s where the scenario expects %s",
				id, pricesOf(got).priceLine(), want.stated.priceLine()))
		}
	}
	return out
}
