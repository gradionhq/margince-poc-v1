// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the offer-draft case owes the certification lane: it issues the request
// the orchestrator issues, it judges the reply with the no-guess gate AND the
// price ladder the orchestrator judges it with, and it separates the three
// things a reply can be. A deal's context is the counterparty's own words, so
// "nothing could be staged" and "the price is wrong" fail for opposite reasons
// and want opposite fixes.
//
// The rate-card rung is exercised here rather than left to the integration lane
// because it is half the gate: a case that stopped at the evidence check would
// certify that a line is grounded while saying nothing about the number on it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The two context items every test below drafts from, and the product the
// second one asks for by name.
const (
	offerDraftKickoffSource = "activity:1"
	offerDraftSupportSource = "activity:2"
	offerDraftSupportPlan   = "Support Plan"
)

// offerDraftDealFixture is one deal as the orchestrator hands it over: a
// conversation that states its own price, a conversation that asks for a
// catalogue product by name, and a rate card carrying that product plus one
// priced in a currency this offer is not written in.
func offerDraftDealFixture() offerDraftFixture {
	return offerDraftFixture{
		Currency: "EUR",
		ContextItems: []offerDraftContextItem{
			{
				SourceID: offerDraftKickoffSource,
				Snippet:  "The client asked for a kickoff workshop and agreed to 20000 cents for it.",
			},
			{
				SourceID: offerDraftSupportSource,
				Snippet:  "They also asked us to quote the standard support plan.",
			},
		},
		RateCard: []offerDraftProduct{
			{Name: offerDraftSupportPlan, UnitPriceMinor: 5000, Currency: "EUR"},
			{Name: "Onsite Day", UnitPriceMinor: 90000, Currency: "USD"},
		},
	}
}

func offerDraftFixtureJSON(t *testing.T, f offerDraftFixture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// offerDraftExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func offerDraftExpectation(t *testing.T, lines map[string]offerDraftExpectedLine) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(lines)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

// offerDraftKickoffExpected is the conversation-priced line: one entry, because
// the expectation is a subset claim and a scenario pins the lines it is about.
func offerDraftKickoffExpected(t *testing.T) json.RawMessage {
	t.Helper()
	return offerDraftExpectation(t, map[string]offerDraftExpectedLine{
		offerDraftKickoffSource: {UnitPriceMinor: 20000, PriceGrounded: true},
	})
}

// offerDraftSupportExpected is the rate-card-priced line, which a draft can only
// reach by citing a product id it read out of this call's own prompt.
func offerDraftSupportExpected(t *testing.T) json.RawMessage {
	t.Helper()
	return offerDraftExpectation(t, map[string]offerDraftExpectedLine{
		offerDraftSupportSource: {UnitPriceMinor: 5000, PriceGrounded: true},
	})
}

// draftedLine renders one candidate as the prompt demands it, plus whichever of
// the two optional price fields a caller puts on it.
func draftedLine(description, evidence, sourceID string, priceFields ...string) string {
	fields := []string{
		`"description":"` + description + `"`,
		`"quantity":"1"`,
		`"tax_rate":"19.00"`,
		`"evidence_snippet":"` + evidence + `"`,
		`"source_id":"` + sourceID + `"`,
	}
	return "{" + strings.Join(append(fields, priceFields...), ",") + "}"
}

func draftReply(lines ...string) string { return `{"lines":[` + strings.Join(lines, ",") + `]}` }

// kickoffLine cites the conversation that states its own price.
func kickoffLine(priceFields ...string) string {
	return draftedLine("Kickoff workshop", "agreed to 20000 cents for it", offerDraftKickoffSource, priceFields...)
}

// supportLine cites the conversation that asks for a catalogue product.
func supportLine(priceFields ...string) string {
	return draftedLine("Support plan", "quote the standard support plan", offerDraftSupportSource, priceFields...)
}

// offerDraftCompleterStub answers with a reply built from the request it was
// handed, because a rate-card line must cite an id that exists only in that
// request: Prepare mints the catalogue's ids exactly as production reads them
// off the products table, so a draft reaches one the same way a model does.
type offerDraftCompleterStub struct{ reply func(model.Request) string }

func (s offerDraftCompleterStub) Complete(_ context.Context, req model.Request) (model.Response, error) {
	return model.Response{Text: s.reply(req)}, nil
}

// cannedReply is the answer that does not need to read anything.
func cannedReply(text string) func(model.Request) string {
	return func(model.Request) string { return text }
}

// catalogIDFor reads a product's id out of the rate-card block the request
// carries — the one place this call's ids are written down.
func catalogIDFor(t *testing.T, req model.Request, name string) string {
	t.Helper()
	for _, line := range strings.Split(req.Messages[0].Content, "\n") {
		if !strings.Contains(line, "] "+name+" @") {
			continue
		}
		id, _, bracketed := strings.Cut(strings.TrimPrefix(line, "["), "]")
		if !bracketed {
			t.Fatalf("the rate-card line %q carries no id", line)
		}
		return id
	}
	t.Fatalf("the request shows no product named %q:\n%s", name, req.Messages[0].Content)
	return ""
}

func prepareOfferDraftCase(t *testing.T, f offerDraftFixture, expected json.RawMessage) *offerDraftCase {
	t.Helper()
	prepared, err := offerDraftCases{}.Prepare(offerDraftFixtureJSON(t, f), expected)
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	c, ok := prepared.(*offerDraftCase)
	if !ok {
		t.Fatalf("Prepare returned %T, want the offer-draft case", prepared)
	}
	return c
}

func runOfferDraftCase(t *testing.T, c *offerDraftCase, reply func(model.Request) string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	trace, err := c.Run(context.Background(), offerDraftCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return c.Evaluate(trace), trace
}

func draftOutcome(
	t *testing.T, expected json.RawMessage, reply func(model.Request) string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	return runOfferDraftCase(t, prepareOfferDraftCase(t, offerDraftDealFixture(), expected), reply)
}

func TestOfferDraftCaseSeparatesTheFourThingsAReplyCanBe(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the line the conversation prices",
			reply:      draftReply(kickoffLine(`"conversation_price_minor":20000`)),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name:       "a citation the context does not say",
			reply:      draftReply(draftedLine("Kickoff workshop", "agreed to a 40% discount", offerDraftKickoffSource)),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: `the gate refused the line "Kickoff workshop"`,
		},
		{
			// This deal's context DOES ground a line, so declining to draft one is
			// still a failed run — but it fails as an abstention, and the record has
			// to keep that apart from a draft the gate emptied.
			name:       "no line drafted at all",
			reply:      draftReply(),
			wantResult: aitasks.OutcomeAbstained,
			wantDetail: `no staged line cites "activity:1"`,
		},
		{
			name:       "a reply that is not the required JSON",
			reply:      "I could not find anything to quote.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: "unparseable",
		},
		{
			// Well formed, evidenced, and priced from nowhere: a measurement of the
			// model, not a defect in the reply.
			name:       "the expected line staged without its price",
			reply:      draftReply(kickoffLine()),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `the line citing "activity:1" is priced 0 minor units, ungrounded`,
		},
		{
			// The ladder's conversation rung needs the amount inside the text the
			// line cites, so a price the model invented drops to the zero sentinel
			// even though the citation itself is real.
			name:       "a price the cited evidence never states",
			reply:      draftReply(kickoffLine(`"conversation_price_minor":30000`)),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `the line citing "activity:1" is priced 0 minor units, ungrounded`,
		},
		{
			name:       "the expected line never drafted",
			reply:      draftReply(supportLine()),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `no staged line cites "activity:1"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := draftOutcome(t, offerDraftKickoffExpected(t), cannedReply(tc.reply))
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// The price ladder is the half of this gate that used to need a database, and
// this is the test that says it still runs: a line prices from the rate card
// only when it cites a product this call was actually shown, in the currency the
// offer is written in.
func TestOfferDraftCasePricesALineOffTheRateCardItWasShown(t *testing.T) {
	cases := []struct {
		name       string
		productFor func(t *testing.T, req model.Request) string
		wantResult string
		wantDetail string
	}{
		{
			name: "the product the conversation asks for",
			productFor: func(t *testing.T, req model.Request) string {
				return catalogIDFor(t, req, offerDraftSupportPlan)
			},
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// An id no catalogue carries is the shape a hallucination takes here,
			// and the ladder answers it with the zero sentinel rather than a price.
			name: "a product id this workspace does not stock",
			productFor: func(*testing.T, model.Request) string {
				return ids.New[ids.ProductKind]().String()
			},
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `the line citing "activity:2" is priced 0 minor units, ungrounded`,
		},
		{
			// A real product priced in another currency must never be stamped
			// grounded with a wrong-currency amount.
			name: "a product priced in another currency",
			productFor: func(t *testing.T, req model.Request) string {
				return catalogIDFor(t, req, "Onsite Day")
			},
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `the line citing "activity:2" is priced 0 minor units, ungrounded`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := func(req model.Request) string {
				return draftReply(supportLine(`"product_id":"` + tc.productFor(t, req) + `"`))
			}

			outcome, _ := draftOutcome(t, offerDraftSupportExpected(t), reply)

			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// A line the gate refused reaches the Detail whatever the result: a draft that
// staged the expected line while inventing one out of nothing is not the clean
// run it would otherwise look like.
func TestOfferDraftCaseReportsARefusedLineBesideAnAcceptedAnswer(t *testing.T) {
	reply := draftReply(
		kickoffLine(`"conversation_price_minor":20000`),
		draftedLine("Invented add-on", "a discount nobody discussed", offerDraftKickoffSource),
	)

	outcome, _ := draftOutcome(t, offerDraftKickoffExpected(t), cannedReply(reply))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, `the gate refused the line "Invented add-on"`) {
		t.Errorf("Detail = %q, want it to name the refused line", outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite the fixture's captured
// text — the canary sweep does exactly that — without rewriting an assertion.
func TestOfferDraftFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(offerDraftFixtureJSON(t, offerDraftDealFixture()), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"currency": true, "context_items": true, "rate_card": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the orchestrator does not hand this call", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which the orchestrator always supplies", name)
		}
	}
}

// A product id is not among them, and this is why: the model is shown the
// catalogue's ids and must cite one back, so an id an author could write into
// the corpus is an id the expected reply could carry without ever having read
// the prompt.
func TestOfferDraftFixtureMintsTheRateCardIDsItself(t *testing.T) {
	var entries []map[string]json.RawMessage
	raw, err := json.Marshal(offerDraftDealFixture().RateCard)
	if err != nil {
		t.Fatalf("encoding the rate card: %v", err)
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decoding the rate card: %v", err)
	}
	for _, entry := range entries {
		if _, present := entry["id"]; present {
			t.Error("a rate-card entry carries an id, which would hand the model the token it must read for itself")
		}
	}
	first := prepareOfferDraftCase(t, offerDraftDealFixture(), offerDraftSupportExpected(t))
	second := prepareOfferDraftCase(t, offerDraftDealFixture(), offerDraftSupportExpected(t))
	if first.catalog[0].Id == second.catalog[0].Id {
		t.Error("two prepared cases share a product id, so an answer could carry one it was never shown")
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheOfferDraftCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := offerDraftCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
