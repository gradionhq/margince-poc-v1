// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the pricing case owes the certification lane: it issues the request the
// crawl issues, it judges the reply with the no-guess gate the crawl judges it
// with, and it separates the three things a reply can be. A page is published by
// someone this system has never met, so "the gate refused everything" and "the
// prices are wrong" fail for opposite reasons and want opposite fixes.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The page every test below crawls: two models, each priced on its own line, so
// numberPassages gives the model two passages to cite.
const pricingPageText = `Aurora AI — Aurora Large, our flagship model. Input $5.00 / 1M tokens, output $25.00 / 1M tokens. Prompt caching: cache reads $0.50 / 1M, cache writes $6.25 / 1M.

Aurora AI — Aurora Mini, fast and cheap. Input $0.25 / 1M tokens, output $1.50 / 1M tokens. Prompt caching is not available on this model.`

// pricingProvider is the CONFIGURED source name — the one the sheet files a
// staged rate under, whatever the page calls its own vendor.
const pricingProvider = "aurora"

func pricingPageFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ratePricingFixture{PageText: pricingPageText, Provider: pricingProvider})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// pricingExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func pricingExpectation(t *testing.T, models map[string]ratePricedModel) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

// largeExpected is the flagship model as the page grounds it: one entry, because
// the expectation is a subset claim and a scenario pins the prices it cares about.
func largeExpected(t *testing.T) json.RawMessage {
	t.Helper()
	return pricingExpectation(t, map[string]ratePricedModel{"aurora-large": {
		InputUsd: "5", OutputUsd: "25", CacheReadUsd: "0.5", CacheWriteUsd: "6.25",
	}})
}

// pricingRow is one row of a model's reply, built as text rather than marshalled
// so a malformed reply is as expressible as a well-formed one.
func pricingRow(id, in, out, cacheRead, cacheWrite, evidence, confidence string) string {
	return `{"provider":"Aurora AI","model_id":"` + id +
		`","input_per_mtok":"` + in + `","output_per_mtok":"` + out +
		`","cache_read_per_mtok":"` + cacheRead + `","cache_write_per_mtok":"` + cacheWrite +
		`","evidence":"` + evidence + `","confidence":"` + confidence + `"}`
}

func pricingReply(rows ...string) string {
	return `{"models":[` + strings.Join(rows, ",") + `]}`
}

// largeRow is the flagship row exactly as the page grounds it.
func largeRow() string { return pricingRow("aurora-large", "5", "25", "0.5", "6.25", "s0", "0.95") }

// miniRow is the second model, which no expectation below names — a real page
// prices more models than a scenario cares to pin.
func miniRow() string { return pricingRow("aurora-mini", "0.25", "1.5", "0", "0", "s1", "0.9") }

// pricingCompleterStub answers with one canned reply. What the case ASKED is
// read off the trace, which is the only copy the certification lane itself has.
type pricingCompleterStub struct{ reply string }

func (s pricingCompleterStub) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// pricingPageStub stands in for the web read: the crawl's own fetcher rightly
// refuses a loopback test server, and what this site is certified on is the page
// text, never the retrieval of it.
type pricingPageStub struct{ text string }

func (f pricingPageStub) Fetch(_ context.Context, _ string) (webread.Doc, error) {
	return webread.Doc{Text: f.text}, nil
}

func runPricingCase(t *testing.T, expected json.RawMessage, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := ratePricingCases{}.Prepare(pricingPageFixture(t), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), pricingCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

func TestRatePricingCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the prices the page grounds",
			reply:      pricingReply(largeRow(), miniRow()),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The sheet stores µUSD, so the comparison is the product's own: a
			// scenario neither fails on a trailing zero nor passes on a rounding.
			name:       "the same price written to two decimals",
			reply:      pricingReply(pricingRow("aurora-large", "5.00", "25.000", "0.50", "6.2500", "s0", "0.9")),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "nothing cited, so nothing survives the gate",
			reply:      pricingReply(pricingRow("aurora-large", "5", "25", "0.5", "6.25", "", "0.95")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `the gate refused the row for "aurora-large"`,
		},
		{
			name:       "no model claimed at all",
			reply:      pricingReply(),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "the model priced nothing at all",
		},
		{
			name:       "a reply that is not the required JSON",
			reply:      "I could not read that page.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
		{
			// Well formed and wrong is a measurement of the model, not a defect
			// in the reply — the opposite fix from every case above it.
			name:       "a price the page does not state",
			reply:      pricingReply(pricingRow("aurora-large", "4", "25", "0.5", "6.25", "s0", "0.95")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `"aurora-large" is priced in 4`,
		},
		{
			name:       "the expected model is never priced",
			reply:      pricingReply(miniRow()),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `no surviving row for "aurora-large"`,
		},
		{
			// The gate admits a row whose prices are not per-MTok decimals; the
			// diff then drops it, so the scenario's price is never staged.
			name:       "a price the sheet cannot read",
			reply:      pricingReply(pricingRow("aurora-large", "$5", "25", "0.5", "6.25", "s0", "0.95")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `prices the sheet cannot read`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runPricingCase(t, largeExpected(t), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A row the gate refused reaches the Detail whatever the result: a reply that
// grounded the expected prices while inventing a model out of nothing is not the
// clean run it would otherwise look like.
func TestRatePricingCaseReportsARefusedRowBesideAnAcceptedAnswer(t *testing.T) {
	reply := pricingReply(largeRow(), pricingRow("invented", "9", "9", "0", "0", "", "0.9"))

	outcome, _ := runPricingCase(t, largeExpected(t), reply)

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, `the gate refused the row for "invented"`) {
		t.Errorf("Detail = %q, want it to name the refused row", outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's page text
// — the canary sweep does exactly that — without rewriting an assertion.
func TestRatePricingFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(pricingPageFixture(t), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"page_text": true, "provider": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the crawl does not hand the extraction", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the crawl always supplies", name)
		}
	}
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same page, answered by the same model
// text, must produce the same request and the same surviving rows in the crawl as
// in the case.
//
// This site is where that proof is cheapest and strongest, because its system
// prompt is separately pinned byte-for-byte to the corpus — so a request equal to
// production's is a request equal to the certified one.
func TestRatePricingCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name string
		// wantKept is what the crawl carries forward to the sheet's diff, and
		// the case owes the same verdict about the same rows.
		reply      string
		wantKept   []string
		wantResult string
	}{
		{
			name: "both models grounded", reply: pricingReply(largeRow(), miniRow()),
			wantKept: []string{"aurora-large", "aurora-mini"}, wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:     "one grounded, one cited nothing",
			reply:    pricingReply(largeRow(), pricingRow("invented", "9", "9", "0", "0", "", "0.9")),
			wantKept: []string{"aurora-large"}, wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:     "nothing the crawl can stage",
			reply:    pricingReply(pricingRow("aurora-large", "5", "25", "0.5", "6.25", "s0", "0.2")),
			wantKept: nil, wantResult: aitasks.OutcomeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			crawl := modelCostRefresh{
				fetcher: pricingPageStub{text: pricingPageText},
				brain:   brain,
				log:     slog.New(slog.DiscardHandler),
			}
			kept, err := crawl.extract(
				context.Background(),
				pricingSource{Provider: pricingProvider, URL: "https://prices.test/pricing"},
			)
			if err != nil {
				t.Fatalf("the crawl refused the reply outright: %v", err)
			}

			outcome, trace := runPricingCase(t, largeExpected(t), tc.reply)

			if len(kept) != len(tc.wantKept) {
				t.Fatalf("the crawl kept %d rows, want %d", len(kept), len(tc.wantKept))
			}
			for i, id := range tc.wantKept {
				if kept[i].ModelID != id {
					t.Errorf("the crawl kept %q at %d, want %q", kept[i].ModelID, i, id)
				}
				if kept[i].Provider != pricingProvider {
					t.Errorf("the crawl filed %q under %q, want the configured source %q",
						kept[i].ModelID, kept[i].Provider, pricingProvider)
				}
			}
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if len(trace.Requests) != 1 {
				t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
			}
			assertSameRateExtractRequest(t, brain.request, trace.Requests[0])
			if trace.Output != tc.reply {
				t.Errorf("the trace records %q, want the model's own reply", trace.Output)
			}
		})
	}
}

// assertSameRateExtractRequest compares two requests for the same page. The fence
// marker is minted per call, so it is normalised away — every other byte of a
// request the certification lane claims production sends must match the one
// production sent.
func assertSameRateExtractRequest(t *testing.T, production, certified model.Request) {
	t.Helper()
	normalize := func(req model.Request) model.Request {
		marker, declared := promptfence.MarkerIn(req.System)
		if !declared {
			t.Fatalf("the request declares no data boundary: %q", req.System)
		}
		out := req
		out.System = strings.ReplaceAll(req.System, marker, "MARKER")
		out.Messages = make([]model.Message, len(req.Messages))
		for i, message := range req.Messages {
			out.Messages[i] = model.Message{
				Role:    message.Role,
				Content: strings.ReplaceAll(message.Content, marker, "MARKER"),
			}
		}
		return out
	}
	production, certified = normalize(production), normalize(certified)
	if production.System != certified.System {
		t.Errorf("the certified system prompt is not production's:\n%q\n%q", certified.System, production.System)
	}
	if len(production.Messages) != len(certified.Messages) {
		t.Fatalf("the certified request carries %d turns, production sent %d",
			len(certified.Messages), len(production.Messages))
	}
	for i, message := range production.Messages {
		if certified.Messages[i] != message {
			t.Errorf("certified turn %d = %+v, production sent %+v", i, certified.Messages[i], message)
		}
	}
	if certified.MaxTokens != production.MaxTokens ||
		string(certified.ResponseSchema) != string(production.ResponseSchema) ||
		certified.SecretStripper == nil {
		t.Errorf("the certified request lost the governed bounds production sends: %+v", certified)
	}
}

// An expectation no reply could satisfy would measure nothing for as long as it
// stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestRatePricingCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		expected json.RawMessage
		wantMsg  string
	}{
		{
			name:     "no model expected at all",
			expected: json.RawMessage(`{}`),
			wantMsg:  "expects no model",
		},
		{
			name:     "an expectation shaped like something else",
			expected: json.RawMessage(`["aurora-large"]`),
			wantMsg:  "model id to its prices",
		},
		{
			name:     "no expectation at all",
			expected: nil,
			wantMsg:  "model id to its prices",
		},
		{
			name: "a model with no id",
			expected: pricingExpectation(t, map[string]ratePricedModel{
				"  ": {InputUsd: "5", OutputUsd: "25", CacheReadUsd: "0", CacheWriteUsd: "0"},
			}),
			wantMsg: "names no model",
		},
		{
			// The gate trims the id the same way the write path does, so a padded
			// key can never match a surviving row.
			name: "a padded model id",
			expected: pricingExpectation(t, map[string]ratePricedModel{
				" aurora-large ": {InputUsd: "5", OutputUsd: "25", CacheReadUsd: "0", CacheWriteUsd: "0"},
			}),
			wantMsg: "the gate trims",
		},
		{
			name: "a price the sheet could never store",
			expected: pricingExpectation(t, map[string]ratePricedModel{
				"aurora-large": {InputUsd: "$5", OutputUsd: "25", CacheReadUsd: "0", CacheWriteUsd: "0"},
			}),
			wantMsg: "not per-MTok decimals",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ratePricingCases{}.Prepare(pricingPageFixture(t), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// The expectation has no provider to name, and this is why: the gate overwrites
// every row's provider with the configured source, so a provider in the corpus
// could only ever restate the fixture or assert something no reply can reach.
// The reply below files itself under a vendor the fixture does not crawl, and the
// case must still accept it — the sheet never stores that name.
func TestRatePricingCaseIgnoresTheProviderAPageClaims(t *testing.T) {
	reply := pricingReply(strings.Replace(largeRow(), `"provider":"Aurora AI"`, `"provider":"evil-corp"`, 1))

	outcome, _ := runPricingCase(t, largeExpected(t), reply)

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Errorf("Result = %q (%s), want accepted — the provider a page claims is not the sheet's",
			outcome.Result, outcome.Detail)
	}
}

// A fixture the crawl could never have produced would certify a call the product
// does not make: PricingSourcesFromMap skips a source with no provider, and a
// page with no text leaves the model no passage to cite.
func TestRatePricingCaseRefusesAFixtureTheCrawlCouldNotRun(t *testing.T) {
	cases := []struct {
		name    string
		fixture ratePricingFixture
		wantMsg string
	}{
		{
			name:    "a page with no text",
			fixture: ratePricingFixture{PageText: "  \n\n ", Provider: pricingProvider},
			wantMsg: "no page text",
		},
		{
			name:    "a source with no provider",
			fixture: ratePricingFixture{PageText: pricingPageText, Provider: " "},
			wantMsg: "names no provider",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.fixture)
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = ratePricingCases{}.Prepare(raw, largeExpected(t))
			if err == nil {
				t.Fatal("a fixture the crawl could not run prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is missing: %v", err)
			}
		})
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheRatePricingCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := ratePricingCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
