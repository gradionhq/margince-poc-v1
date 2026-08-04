// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the page-fact case owes the certification lane: it issues the request one
// page of the deep read issues — under that page kind's own menu — it judges the
// reply with the no-guess gate that lane judges it with, and it separates a reply
// nothing survived from one that survived and says the wrong thing. Those two
// want opposite fixes, and a case collapsing them could report neither.
//
// Two things vary per call, so no assertion below pins either: the menu comes
// from the fixture's kind, and every citation is written by asking the case's own
// index which passage carries a phrase. A hard-coded snippet id would be an
// assertion about the passage packer instead of the citation.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The page every case below reads: a catalog page stating two offers, long
// enough that the packer keeps them apart — the condition under which a
// citation can be wrong about which offer it points at.
const (
	sitePageFactsFirstOffer = "Cloud Cost Audit — a line-by-line review of cloud spend that identifies waste " +
		"across compute, storage and networking budgets, delivered in four weeks with a written report for the finance team."
	sitePageFactsSecondOffer = "Kubernetes Migration moves stateful workloads onto managed clusters for " +
		"manufacturing companies across the DACH region, with runbooks, on-call training and a rollback plan agreed " +
		"before any traffic shifts."
	sitePageFactsText = sitePageFactsFirstOffer + "\n" + sitePageFactsSecondOffer
	sitePageFactsURL  = seedURL + "/services"
)

// sitePageFactsCompleterStub answers with the canned reply a run is about. What
// the model was asked reaches the assertions through the trace, which is where
// the record and the canary gate read it from too.
type sitePageFactsCompleterStub struct{ reply string }

func (s sitePageFactsCompleterStub) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// sitePageFactsReply is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
func sitePageFactsReply(claims ...string) string {
	return `{"facts":[` + strings.Join(claims, ",") + `]}`
}

// sitePageFactsClaim is one fact in the compact shape this prompt demands.
func sitePageFactsClaim(field, value, snippetID string) string {
	return fmt.Sprintf(`{"f":%q,"v":%q,"e":%q}`, field, value, snippetID)
}

// sitePageFactsSnippetID numbers the page the way the case does and returns the
// id of the first passage carrying the given text. The packer decides where a
// page splits, so a citation is computed rather than written down.
func sitePageFactsSnippetID(t *testing.T, kind crmcontracts.SiteReadPageKind, text, contains string) string {
	t.Helper()
	idx := newSnippetIndex([]crawlPage{{URL: sitePageFactsURL, Kind: kind, Text: text}})
	for i, ref := range idx.refs {
		if strings.Contains(ref.passage, contains) {
			return fmt.Sprintf("s%d", i)
		}
	}
	t.Fatalf("no passage of the fixture carries %q", contains)
	return ""
}

// sitePageFactsCatalogID names the catalog passage carrying a phrase.
func sitePageFactsCatalogID(t *testing.T, contains string) string {
	t.Helper()
	return sitePageFactsSnippetID(t, crmcontracts.SiteReadPageKindServices, sitePageFactsText, contains)
}

// sitePageFactsJSON encodes a fixture or an expectation as the corpus carries
// them: two documents side by side, never one nested in the other.
func sitePageFactsJSON[T any](t *testing.T, v T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding %T: %v", v, err)
	}
	return raw
}

func sitePageFactsFixtureJSON(t *testing.T, kind crmcontracts.SiteReadPageKind, text string) json.RawMessage {
	t.Helper()
	return sitePageFactsJSON(t, sitePageFactsFixture{URL: sitePageFactsURL, Kind: kind, Text: text})
}

func sitePageFactsCatalogFixture(t *testing.T) json.RawMessage {
	t.Helper()
	return sitePageFactsFixtureJSON(t, crmcontracts.SiteReadPageKindServices, sitePageFactsText)
}

func runSitePageFactsCase(t *testing.T, fixture json.RawMessage, want map[string]string, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := sitePageFactsCases{}.Prepare(fixture, sitePageFactsJSON(t, want))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), sitePageFactsCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

// gatekit:fixture the fact value this case's reply is graded against — expected
// data the case owns, not a waived exception.
var sitePageFactsWantAudit = map[string]string{people.FactService: "Cloud Cost Audit"}

// sitePageFactsReplyCase is one reply and the outcome this site owes it. Each
// result has a test: a fabricating model is a prompt problem, a confidently
// wrong one is a model choice, a silent one is neither, and a run reporting
// them as one number could diagnose none of the three.
type sitePageFactsReplyCase struct {
	name       string
	want       map[string]string
	reply      string
	wantDetail string
}

func runSitePageFactsReplyCases(t *testing.T, wantResult string, cases []sitePageFactsReplyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runSitePageFactsCase(t, sitePageFactsCatalogFixture(t), tc.want, tc.reply)
			if outcome.Result != wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

func TestSitePageFactsCaseAcceptsWhatItGroundsAndTheScenarioExpects(t *testing.T) {
	runSitePageFactsReplyCases(t, aitasks.OutcomeAccepted, []sitePageFactsReplyCase{
		{
			// The page decides whether a list item carries a description, so the
			// scenario names the item and the description does not decide the verdict.
			name:  "a list fact whose description the scenario never named",
			want:  sitePageFactsWantAudit,
			reply: sitePageFactsReply(sitePageFactsAuditClaim(t, "Cloud Cost Audit — a four-week review")),
		},
		{
			// A page states several offers and a scenario names one of them: the row
			// the scenario named is the row it is asking about, wherever it lands.
			name: "one of several rows a multi-value field grounds",
			want: sitePageFactsWantAudit,
			reply: sitePageFactsReply(
				sitePageFactsClaim(people.FactService, "Kubernetes Migration — managed clusters",
					sitePageFactsCatalogID(t, "Kubernetes Migration")),
				sitePageFactsAuditClaim(t, "Cloud Cost Audit — a four-week review")),
		},
	})
}

// Unusable is the fact lane refusing everything it claimed: production stores no
// fact from this page at all, and the reply is what made that happen.
func TestSitePageFactsCaseReportsAReplyTheGateRefusesEntirely(t *testing.T) {
	runSitePageFactsReplyCases(t, aitasks.OutcomeInvalid, []sitePageFactsReplyCase{
		{
			// An offer the page never names is the shape a fabricating model takes
			// here, and the citation gate is what refuses it.
			name:       "a value the cited passage never names",
			want:       sitePageFactsWantAudit,
			reply:      sitePageFactsReply(sitePageFactsAuditClaim(t, "Phishing Simulation — never on this page")),
			wantDetail: dropValueNotInSnippet,
		},
		{
			// The schema enum makes this unreachable for a provider that honours it.
			name: "a citation outside this call's own index",
			want: sitePageFactsWantAudit,
			reply: sitePageFactsReply(sitePageFactsClaim(people.FactService,
				"Cloud Cost Audit — a four-week review", "s99")),
			wantDetail: dropValueNotInSnippet,
		},
		{
			// The field enum is this page kind's menu: a company fact on a catalog
			// page is a field the model was never offered.
			name: "a field this page's menu never offered",
			want: sitePageFactsWantAudit,
			reply: sitePageFactsReply(sitePageFactsClaim(people.FactFoundedYear, "1998",
				sitePageFactsCatalogID(t, "Cloud Cost Audit"))),
			wantDetail: dropUnknownField,
		},
		{
			name:       "a reply that is not the required JSON",
			want:       sitePageFactsWantAudit,
			reply:      "I could not read this page.",
			wantDetail: dropUnparseableReply,
		},
	})
}

// Wrong is a usable reply that says something else: a model measurement.
func TestSitePageFactsCaseReportsAGroundedReplyTheScenarioDisagreesWith(t *testing.T) {
	runSitePageFactsReplyCases(t, aitasks.OutcomeWrongAnswer, []sitePageFactsReplyCase{
		{
			name: "a grounded row the scenario disagrees with",
			want: sitePageFactsWantAudit,
			reply: sitePageFactsReply(sitePageFactsClaim(people.FactService,
				"Kubernetes Migration — managed clusters", sitePageFactsCatalogID(t, "Kubernetes Migration"))),
			wantDetail: "Cloud Cost Audit",
		},
		{
			// A page grounds a field the model simply never returns, and a run that
			// grounded something else is still a usable reply.
			name: "an expected field the reply never mentions",
			want: map[string]string{
				people.FactService:   "Cloud Cost Audit",
				people.FactGeography: "DACH region",
			},
			reply:      sitePageFactsReply(sitePageFactsAuditClaim(t, "Cloud Cost Audit — a four-week review")),
			wantDetail: "no surviving " + people.FactGeography,
		},
	})
}

// Omission is what this prompt asks for when the page states nothing, so a reply
// claiming nothing in any lane is an abstention, never invalid — that is the
// word for the fabricating model whose every claim the gate refused. Against a
// page that DOES list services it is still a failed run, and the disagreement
// says so.
func TestSitePageFactsCaseReportsAReplyThatGroundsNothingAsAnAbstention(t *testing.T) {
	runSitePageFactsReplyCases(t, aitasks.OutcomeAbstained, []sitePageFactsReplyCase{{
		name:       "a page the scenario says lists a service",
		want:       sitePageFactsWantAudit,
		reply:      sitePageFactsReply(),
		wantDetail: "no surviving " + people.FactService,
	}})
}

// sitePageFactsAuditClaim cites the passage carrying the first offer, which is
// the citation a model reading this page would make.
func sitePageFactsAuditClaim(t *testing.T, value string) string {
	t.Helper()
	return sitePageFactsClaim(people.FactService, value, sitePageFactsCatalogID(t, "Cloud Cost Audit"))
}

// A single-value fact states one thing, so all of it is the claim — unlike a list
// item, whose description the page supplies and the scenario cannot predict.
func TestSitePageFactsCaseHoldsASingleValueFactToItsWholeValue(t *testing.T) {
	const contact = "Rufen Sie uns an unter 0711 123456 oder schreiben Sie an hallo@acme.example. " +
		"Unser Buero in Stuttgart ist werktags von 9 bis 17 Uhr besetzt."
	fixture := sitePageFactsFixtureJSON(t, crmcontracts.SiteReadPageKindContact, contact)
	cited := sitePageFactsSnippetID(t, crmcontracts.SiteReadPageKindContact, contact, "0711 123456")
	want := map[string]string{people.FactPhone: "0711 123456"}

	outcome, _ := runSitePageFactsCase(t, fixture, want,
		sitePageFactsReply(sitePageFactsClaim(people.FactPhone, "0711 123456", cited)))
	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want the stated number accepted", outcome.Result, outcome.Detail)
	}

	outcome, _ = runSitePageFactsCase(t, fixture, want,
		sitePageFactsReply(sitePageFactsClaim(people.FactPhone, "0711 123456 — Zentrale", cited)))
	if outcome.Result != aitasks.OutcomeWrongAnswer {
		t.Fatalf("Result = %q (%s), want a single-value fact held to all of its value",
			outcome.Result, outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion. A crawled page's byte count and fetch duration reach the debug
// report alone, so a fixture carrying them would describe a crawl.
func TestSitePageFactsFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var page map[string]json.RawMessage
	if err := json.Unmarshal(sitePageFactsCatalogFixture(t), &page); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"url": true, "kind": true, "text": true}
	for name := range page {
		if !given[name] {
			t.Errorf("the fixture carries %q, which reaches neither the prompt nor the gate", name)
		}
	}
	for name := range given {
		if _, present := page[name]; !present {
			t.Errorf("the fixture drops %q, which the menu routing or the resolver reads", name)
		}
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect — and this site's request is where BOTH per-call axes are proved: the
// menu the kind routes to, and the ids the gate will resolve.
func TestSitePageFactsCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runSitePageFactsCase(t, sitePageFactsCatalogFixture(t), sitePageFactsWantAudit,
		sitePageFactsReply(sitePageFactsAuditClaim(t, "Cloud Cost Audit — a four-week review")))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this page issues", len(trace.Requests))
	}
	req := trace.Requests[0]
	if !strings.Contains(req.System, "You extract company facts from ONE page") {
		t.Errorf("the traced request is not the page-facts prompt: %q", req.System)
	}
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	content := req.Messages[0].Content
	if !strings.Contains(content, "<"+marker+">") || !strings.Contains(content, "</"+marker+">") {
		t.Errorf("the page is not wrapped in the declared boundary:\n%s", content)
	}
	instructions := outsideEverySpan(content, marker)
	for _, text := range []string{sitePageFactsFirstOffer, sitePageFactsSecondOffer, sitePageFactsURL} {
		if strings.Contains(instructions, text) {
			t.Errorf("crawled text %q reaches the instruction region:\n%s", text, instructions)
		}
	}
	// The id enum is this call's index and the field enum is this kind's menu.
	// A fixed one would let the model cite a passage the gate reads as another,
	// or claim a field this page was never asked for.
	schemaJSON := string(req.ResponseSchema)
	for _, id := range []string{"s0", "s1"} {
		if !strings.Contains(schemaJSON, `"`+id+`"`) {
			t.Errorf("the schema does not offer %q, which this fixture's index carries: %s", id, schemaJSON)
		}
	}
	if strings.Contains(schemaJSON, `"s2"`) {
		t.Errorf("the schema offers an id this fixture's index does not carry: %s", schemaJSON)
	}
	if !strings.Contains(schemaJSON, `"`+people.FactService+`"`) {
		t.Errorf("the schema does not offer %q, which a catalog page's menu carries: %s", people.FactService, schemaJSON)
	}
	if strings.Contains(schemaJSON, `"`+people.FactFoundedYear+`"`) {
		t.Errorf("the schema offers %q, which a catalog page's menu never carries: %s", people.FactFoundedYear, schemaJSON)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the gate to read")
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheSitePageFactsCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := sitePageFactsCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
