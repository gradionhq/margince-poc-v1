// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the FX case owes the certification lane: it issues the request the refresh
// issues, it judges the reply with collect — the refresh's own gate, anchor and
// arithmetic — and it separates the three things a reply can be. A rates page is
// published by someone this system has never met, so "nothing survived the gate"
// and "the rate is wrong" fail for opposite reasons and want opposite fixes.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The page every test below reads: two rates, each on its own line, stated in
// opposite directions — the second must be inverted to reach the base, which is
// the arithmetic the prompt forbids the model and the anchor performs.
const fxPageText = `Live reference rates, refreshed hourly.

1 EUR = 1.0850 USD

1 USD = 0.8000 GBP`

const (
	// fxBase is the workspace base every collected rate is expressed against.
	fxBase = "USD"
	// fxEURRate and fxGBPRate are the two rates as the SHEET holds them: 1 unit of
	// the currency in the base. GBP is the inverse of the page's own direction.
	fxEURRate = "1.085"
	fxGBPRate = "1.25"
)

func fxRatesFixture(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(rateFxFixture{
		PageText:          fxPageText,
		BaseCurrency:      fxBase,
		TrackedCurrencies: []string{"EUR", "GBP"},
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// fxExpectation is what the corpus asserts, encoded as the corpus will carry it —
// beside the fixture, never inside it.
func fxExpectation(t *testing.T, rates map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(rates)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

// eurExpected is the one rate stated in the page's own direction. One entry,
// because the expectation is a subset claim and a scenario pins the rates it
// cares about.
func eurExpected(t *testing.T) json.RawMessage {
	t.Helper()
	return fxExpectation(t, map[string]string{"EUR": fxEURRate})
}

// fxPair is one pair of a model's reply, built as text rather than marshalled so
// a malformed reply is as expressible as a well-formed one.
func fxPair(from, to, rate, evidence, confidence string) string {
	return `{"from_currency":"` + from + `","to_currency":"` + to +
		`","rate":"` + rate + `","evidence":"` + evidence +
		`","confidence":"` + confidence + `"}`
}

func fxRatesReply(pairs ...string) string {
	return `{"pairs":[` + strings.Join(pairs, ",") + `]}`
}

// eurPair and gbpPair are the two rates exactly as the page states them.
func eurPair() string { return fxPair("EUR", "USD", "1.0850", "s1", "0.95") }
func gbpPair() string { return fxPair("USD", "GBP", "0.8000", "s2", "0.9") }

// fxCompleterStub answers with one canned reply. What the case ASKED is read off
// the trace, which is the only copy the certification lane itself has.
type fxCompleterStub struct{ reply string }

func (s fxCompleterStub) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

func runFxCase(t *testing.T, expected json.RawMessage, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := rateFxCases{}.Prepare(fxRatesFixture(t), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), fxCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// fxOutcomeCase is one canned reply and the verdict this site owes it.
type fxOutcomeCase struct {
	name       string
	reply      string
	wantResult string
	wantDetail string
}

// fxOutcomeCases walks the three things a reply can be, from the two rates the
// page grounds down to a reply the refresh cannot read at all.
func fxOutcomeCases() []fxOutcomeCase {
	return []fxOutcomeCase{
		{
			name:       "the rates the page states",
			reply:      fxRatesReply(eurPair(), gbpPair()),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The sheet stores a decimal, so the comparison is the product's own: a
			// scenario neither fails on a trailing zero nor passes on a rounding.
			name:       "the same rate written to more decimals",
			reply:      fxRatesReply(fxPair("EUR", "USD", "1.08500000", "s1", "0.9"), gbpPair()),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "nothing cited, so nothing survives the gate",
			reply:      fxRatesReply(fxPair("EUR", "USD", "1.0850", "", "0.95")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "dropped an ungrounded or low-confidence pair",
		},
		{
			name:       "a confidence this build does not believe",
			reply:      fxRatesReply(fxPair("EUR", "USD", "1.0850", "s1", "0.2")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "dropped an ungrounded or low-confidence pair",
		},
		{
			name:       "no pair priced at all",
			reply:      fxRatesReply(),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "priced no rate for a requested currency",
		},
		{
			name:       "a rate the sheet cannot store",
			reply:      fxRatesReply(fxPair("EUR", "USD", "one point oh eight", "s1", "0.95")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "dropped a pair with an unusable rate",
		},
		{
			name:       "a reply that is not the required JSON",
			reply:      "I could not read that page.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
		{
			// Well formed and wrong is a measurement of the model, not a defect in
			// the reply — the opposite fix from every case above it.
			name:       "a rate the page does not state",
			reply:      fxRatesReply(fxPair("EUR", "USD", "1.2000", "s1", "0.95")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `"EUR" is priced 1.2000000000 against the base where the scenario expects 1.085`,
		},
		{
			// The prompt forbids the model any arithmetic precisely because a
			// swapped direction survives every shape check and reads as a rate.
			name:       "the direction reported backwards",
			reply:      fxRatesReply(fxPair("USD", "EUR", "1.0850", "s1", "0.95")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `"EUR" is priced 0.9216589862 against the base`,
		},
		{
			name:       "the expected currency is never priced",
			reply:      fxRatesReply(gbpPair()),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `no rate for "EUR"`,
		},
	}
}

func TestRateFxCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	for _, tc := range fxOutcomeCases() {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runFxCase(t, eurExpected(t), tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A pair the refresh dropped reaches the Detail whatever the result: a reply that
// read the expected rate while stating a cross-pair nothing can anchor is not the
// clean run it would otherwise look like.
func TestRateFxCaseReportsADroppedPairBesideAnAcceptedAnswer(t *testing.T) {
	reply := fxRatesReply(eurPair(), gbpPair(), fxPair("EUR", "GBP", "0.8500", "s3", "0.9"))

	outcome, _ := runFxCase(t, eurExpected(t), reply)

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, "dropped a cross-pair not anchorable to base") {
		t.Errorf("Detail = %q, want it to name the cross-pair", outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's page text
// — the canary sweep does exactly that — without rewriting an assertion.
func TestRateFxFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fxRatesFixture(t), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"page_text": true, "base_currency": true, "tracked_currencies": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the refresh does not hand the extraction", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the refresh always supplies", name)
		}
	}
}

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same page, answered by the same model
// text, must produce the same request and the same collected rates in the refresh
// as in the case.
//
// The refresh is driven through extract and collect rather than run, because run
// reads the sheet and stages approvals — two database steps that decide nothing
// about the reply. Everything between the page and the rates the sheet would
// diff is exercised here.
func TestRateFxCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name string
		// wantCollected is what the refresh carries forward to its diff, and the
		// case owes the same verdict about the same rates.
		reply         string
		wantCollected map[string]string
		wantResult    string
	}{
		{
			name: "both rates grounded", reply: fxRatesReply(eurPair(), gbpPair()),
			wantCollected: map[string]string{"EUR": "1.0850000000", "GBP": "1.2500000000"},
			wantResult:    aitasks.OutcomeAccepted,
		},
		{
			name:          "one grounded, one cited nothing",
			reply:         fxRatesReply(eurPair(), fxPair("USD", "GBP", "0.8000", "", "0.9")),
			wantCollected: map[string]string{"EUR": "1.0850000000"},
			wantResult:    aitasks.OutcomeAccepted,
		},
		{
			name:          "a currency the sheet does not track",
			reply:         fxRatesReply(eurPair(), fxPair("USD", "JPY", "150", "s4", "0.9")),
			wantCollected: map[string]string{"EUR": "1.0850000000"},
			wantResult:    aitasks.OutcomeAccepted,
		},
		{
			name:          "nothing the refresh can stage",
			reply:         fxRatesReply(fxPair("EUR", "USD", "1.0850", "s1", "0.2")),
			wantCollected: map[string]string{},
			wantResult:    aitasks.OutcomeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brain := &replyBrainStub{response: model.Response{Text: tc.reply}}
			refresh := fxRefresh{
				fetcher: pricingPageStub{text: fxPageText},
				brain:   brain,
				url:     "https://rates.test/reference",
				log:     discardLog(),
			}
			pairs, err := refresh.extract(context.Background())
			if err != nil {
				t.Fatalf("the refresh refused the reply outright: %v", err)
			}
			collected := refresh.collect(fxBase, pairs, trackedCurrencySet([]string{"EUR", "GBP"}))

			outcome, trace := runFxCase(t, eurExpected(t), tc.reply)

			if len(collected) != len(tc.wantCollected) {
				t.Fatalf("the refresh collected %v, want %v", collected, tc.wantCollected)
			}
			for currency, rate := range tc.wantCollected {
				if collected[currency] != rate {
					t.Errorf("the refresh collected %s=%q, want %q", currency, collected[currency], rate)
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

// An expectation no reply could satisfy would measure nothing for as long as it
// stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestRateFxCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		expected json.RawMessage
		wantMsg  string
	}{
		{
			name:     "no currency expected at all",
			expected: json.RawMessage(`{}`),
			wantMsg:  "expects no currency",
		},
		{
			name:     "an expectation shaped like something else",
			expected: json.RawMessage(`["EUR"]`),
			wantMsg:  "currency code to its rate",
		},
		{
			name:     "no expectation at all",
			expected: nil,
			wantMsg:  "currency code to its rate",
		},
		{
			name:     "an entry with no currency",
			expected: fxExpectation(t, map[string]string{"  ": fxEURRate}),
			wantMsg:  "names no currency",
		},
		{
			// The anchor normalizes the code the same way the tracked set is
			// normalized, so a lower-cased key can never match a collected rate.
			name:     "a lower-cased currency code",
			expected: fxExpectation(t, map[string]string{" eur ": fxEURRate}),
			wantMsg:  "upper-cases and trims",
		},
		{
			// collect prices only what the sheet asked for, so a currency outside
			// the fixture's tracked set is dropped before it can be compared.
			name:     "a currency this sheet does not track",
			expected: fxExpectation(t, map[string]string{"JPY": "150"}),
			wantMsg:  "does not track",
		},
		{
			name:     "a rate that is not a decimal",
			expected: fxExpectation(t, map[string]string{"EUR": "about one euro ten"}),
			wantMsg:  "not a rate the sheet can store",
		},
		{
			name:     "a rate that rounds away to nothing",
			expected: fxExpectation(t, map[string]string{"EUR": "0.00000000001"}),
			wantMsg:  "not a rate the sheet can store",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rateFxCases{}.Prepare(fxRatesFixture(t), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// A fixture the refresh could never have produced would certify a call the
// product does not make: a run with nothing tracked no-ops before the model, the
// base is never among the currencies a page is asked to price, and a page with no
// text leaves the model no passage to cite.
func TestRateFxCaseRefusesAFixtureTheRefreshCouldNotRun(t *testing.T) {
	cases := []struct {
		name    string
		fixture rateFxFixture
		wantMsg string
	}{
		{
			name:    "a page with no text",
			fixture: rateFxFixture{PageText: "  \n\n ", BaseCurrency: fxBase, TrackedCurrencies: []string{"EUR"}},
			wantMsg: "no page text",
		},
		{
			name:    "a sheet with no base",
			fixture: rateFxFixture{PageText: fxPageText, BaseCurrency: " ", TrackedCurrencies: []string{"EUR"}},
			wantMsg: "no base currency",
		},
		{
			name:    "a sheet tracking nothing",
			fixture: rateFxFixture{PageText: fxPageText, BaseCurrency: fxBase},
			wantMsg: "tracks no currency",
		},
		{
			name:    "a tracked entry naming no currency",
			fixture: rateFxFixture{PageText: fxPageText, BaseCurrency: fxBase, TrackedCurrencies: []string{" "}},
			wantMsg: "names no currency",
		},
		{
			name: "a sheet tracking its own base",
			fixture: rateFxFixture{
				PageText: fxPageText, BaseCurrency: fxBase, TrackedCurrencies: []string{"EUR", "usd"},
			},
			wantMsg: "tracks its own base",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.fixture)
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = rateFxCases{}.Prepare(raw, eurExpected(t))
			if err == nil {
				t.Fatal("a fixture the refresh could not run prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is missing: %v", err)
			}
		})
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheRateFxCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := rateFxCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
