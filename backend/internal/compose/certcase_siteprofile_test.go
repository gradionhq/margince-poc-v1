// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the profile case owes the certification lane: it issues the request the
// deep read issues, it judges the reply with the citation gate the deep read
// judges it with, and it separates a reply nothing survived from one that
// survived and says the wrong thing. The two want opposite fixes — a fabricating
// model is a prompt problem, a confidently-wrong one is a model choice — and a
// case that collapsed them could report neither.
//
// The numbered index is the sharp edge here, so every assertion below names its
// passage by content and lets the case number it: a test that hard-coded an id
// would be asserting about the excerpt ranking instead of the citation.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The crawl every case below reads: a home page stating what the company sells
// and an imprint naming the entity, which is the shape the profile lane is built
// for — paraphrase evidence on one page, the verbatim legal trio on another.
const (
	siteProfileHomeText = "Acme ships industrial robots and automation lines for manufacturers " +
		"across Europe since 1998, with in-house engineering."
	siteProfileImprintText = "Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. " +
		"USt-ID DE123456789 nach Paragraf 27a UStG."
)

func siteProfileFixturePages() []siteProfilePage {
	return []siteProfilePage{
		{URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome, Text: siteProfileHomeText},
		{URL: seedURL + "/impressum", Kind: crmcontracts.SiteReadPageKindImpressum, Text: siteProfileImprintText},
	}
}

// siteProfileCompleterStub answers with the canned reply a run is about. What
// the model was asked reaches the assertions through the trace, which is where
// the record and the canary gate read it from too.
type siteProfileCompleterStub struct {
	reply string
}

func (s *siteProfileCompleterStub) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// siteProfileReply is the raw text a model returns, built as text rather than
// marshalled so a malformed reply is as expressible as a well-formed one.
func siteProfileReply(claims ...string) string {
	return `{"fields":[` + strings.Join(claims, ",") + `]}`
}

// siteProfileClaim is one grounded field in the shape the profile prompt demands.
// The confidence sits inside the gate's (0,1] range throughout, so a case that
// measures citation is never failing on a number instead.
func siteProfileClaim(field, value, snippetID string) string {
	return fmt.Sprintf(`{"f":%q,"v":%q,"e":%q,"c":0.9}`, field, value, snippetID)
}

// siteProfileSnippetID numbers the fixture the way the case does and returns the
// id of the first passage carrying the given text. Computed rather than written
// down because the excerpt selection ranks the imprint ahead of the home page,
// and a claim citing a hard-coded id would assert about that ranking.
func siteProfileSnippetID(t *testing.T, contains string) string {
	t.Helper()
	pages := make([]crawlPage, 0, 2)
	for _, p := range siteProfileFixturePages() {
		pages = append(pages, crawlPage{URL: p.URL, Kind: p.Kind, Text: p.Text})
	}
	idx := newSnippetIndex(profileExcerptPages(pages))
	for i, ref := range idx.refs {
		if strings.Contains(ref.passage, contains) {
			return fmt.Sprintf("s%d", i)
		}
	}
	t.Fatalf("no passage of the fixture carries %q", contains)
	return ""
}

func siteProfileFixtureJSON(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(siteProfileFixture{Pages: siteProfileFixturePages()})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// siteProfileExpectation is what the corpus asserts, encoded as the corpus will
// carry it — beside the fixture, never inside it.
func siteProfileExpectation(t *testing.T, want map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runSiteProfileCase(t *testing.T, want map[string]string, reply string) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), siteProfileExpectation(t, want))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &siteProfileCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

var siteProfileWantLegalName = map[string]string{
	string(crmcontracts.ColdStartFieldFieldLegalName): "Acme Robotics GmbH",
}

// siteProfileReplyCase is one reply and the outcome this site owes it. The three
// results have a test each, because what separates them is the whole claim: a
// fabricating model is a prompt problem and a confidently-wrong one is a model
// choice, and a run that reported them as one number could diagnose neither.
type siteProfileReplyCase struct {
	name       string
	want       map[string]string
	reply      string
	wantDetail string
}

func runSiteProfileReplyCases(t *testing.T, wantResult string, cases []siteProfileReplyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runSiteProfileCase(t, tc.want, tc.reply)
			if outcome.Result != wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

func TestSiteProfileCaseAcceptsWhatItGroundsAndTheScenarioExpects(t *testing.T) {
	runSiteProfileReplyCases(t, aitasks.OutcomeAccepted, []siteProfileReplyCase{
		{
			// The verbatim half: the entity is named in the passage cited for it.
			name:  "a legal name the cited passage carries",
			want:  siteProfileWantLegalName,
			reply: siteProfileReply(siteProfileLegalNameClaim(t, "Acme Robotics GmbH")),
		},
		{
			// The paraphrase half: the value is the site's substance in the model's
			// own words, cited to the passage that carries it.
			name: "a paraphrase grounded on the page it cites",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldValueProposition): "industrial robots and automation lines",
			},
			reply: siteProfileReply(siteProfileClaim(
				string(crmcontracts.ColdStartFieldFieldValueProposition),
				"industrial robots and automation lines", siteProfileSnippetID(t, "industrial robots"))),
		},
	})
}

// Unusable is the gate refusing everything the reply claimed: production stores
// no profile field at all, and the reply is what made that happen.
func TestSiteProfileCaseReportsAReplyTheGateRefusesEntirely(t *testing.T) {
	runSiteProfileReplyCases(t, aitasks.OutcomeInvalid, []siteProfileReplyCase{
		{
			// An entity the imprint never names is the shape a fabricating model takes
			// here, and the hard gate is what refuses it.
			name:       "a legal name the cited passage never carries",
			want:       siteProfileWantLegalName,
			reply:      siteProfileReply(siteProfileLegalNameClaim(t, "Beispiel Holding AG")),
			wantDetail: dropValueNotInSnippet,
		},
		{
			// The schema enum makes this unreachable for a provider that honours it,
			// and the gate is what answers when one does not.
			name: "a citation outside this call's own index",
			want: siteProfileWantLegalName,
			reply: siteProfileReply(siteProfileClaim(
				string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH", "s99")),
			wantDetail: dropSnippetIDUnknown,
		},
		{
			name:       "a reply that is not the required JSON",
			want:       siteProfileWantLegalName,
			reply:      "I could not read these pages.",
			wantDetail: dropUnparseableReply,
		},
	})
}

// Wrong is a usable reply that says something else — a measurement of the model,
// not a defect in the reply.
func TestSiteProfileCaseReportsAGroundedReplyTheScenarioDisagreesWith(t *testing.T) {
	runSiteProfileReplyCases(t, aitasks.OutcomeWrongAnswer, []siteProfileReplyCase{
		{
			name:       "a grounded value the scenario disagrees with",
			want:       siteProfileWantLegalName,
			reply:      siteProfileReply(siteProfileLegalNameClaim(t, "Acme Robotics")),
			wantDetail: "Acme Robotics GmbH",
		},
		{
			// A crawl can ground a field the model simply never returns, and a run
			// that grounded something else is still a usable reply.
			name: "an expected field the reply never mentions",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldLegalName):        "Acme Robotics GmbH",
				string(crmcontracts.ColdStartFieldFieldValueProposition): "industrial robots and automation lines",
			},
			reply:      siteProfileReply(siteProfileLegalNameClaim(t, "Acme Robotics GmbH")),
			wantDetail: "no surviving " + string(crmcontracts.ColdStartFieldFieldValueProposition),
		},
		{
			// Omission is what this prompt asks for when the passages ground nothing,
			// so a reply claiming none of the eleven is an answer rather than a
			// breakage — and a wrong one wherever a scenario says the passages do
			// ground something.
			name:       "a reply that claims nothing at all",
			want:       siteProfileWantLegalName,
			reply:      siteProfileReply(),
			wantDetail: "no surviving " + string(crmcontracts.ColdStartFieldFieldLegalName),
		},
	})
}

// siteProfileLegalNameClaim claims the given entity on the passage that carries
// the imprint, which is the citation a model reading this crawl would make.
func siteProfileLegalNameClaim(t *testing.T, value string) string {
	t.Helper()
	return siteProfileClaim(
		string(crmcontracts.ColdStartFieldFieldLegalName), value, siteProfileSnippetID(t, "Acme Robotics GmbH"))
}

// The overlap check on a paraphrase field is warning-class: the field survives,
// because a German passage rendered into an English value shares nothing
// lexically and the lane wants that value. The warning still has to reach the
// Detail, or a record would report a model paraphrasing off its own citations as
// a healthy one.
func TestSiteProfileCaseReportsTheWarningClassOverlapItAccepts(t *testing.T) {
	outcome, _ := runSiteProfileCase(t, siteProfileWantLegalName, siteProfileReply(
		siteProfileClaim(
			string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH",
			siteProfileSnippetID(t, "Acme Robotics GmbH")),
		siteProfileClaim(
			string(crmcontracts.ColdStartFieldFieldIcp), "Fertigungsbetriebe in der DACH-Region",
			siteProfileSnippetID(t, "industrial robots")),
	))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want a low-overlap paraphrase to survive", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, dropParaphraseLowOverlap) {
		t.Errorf("Detail = %q, want it to name the paraphrase the gate warned about", outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion. A crawled page's byte count and fetch duration reach the debug
// report alone, so a fixture carrying them would describe a crawl rather than
// the prompt this site certifies.
func TestSiteProfileFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(siteProfileFixtureJSON(t), &top); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if len(top) != 1 {
		t.Errorf("the fixture carries %d keys, want the pages alone: %v", len(top), top)
	}
	pages := []map[string]json.RawMessage{}
	if err := json.Unmarshal(top["pages"], &pages); err != nil {
		t.Fatalf("decoding the fixture's pages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("the fixture carries no page to check")
	}
	given := map[string]bool{"url": true, "kind": true, "text": true}
	for _, page := range pages {
		for name := range page {
			if !given[name] {
				t.Errorf("a fixture page carries %q, which reaches neither the prompt nor the gate", name)
			}
		}
		for name := range given {
			if _, present := page[name]; !present {
				t.Errorf("a fixture page drops %q, which the excerpt selection or the resolver reads", name)
			}
		}
	}
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect — and this site's request is where the per-call schema is proved, since
// the ids it offers are the ids the gate will resolve.
func TestSiteProfileCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	outcome, trace := runSiteProfileCase(t, siteProfileWantLegalName, siteProfileReply(siteProfileClaim(
		string(crmcontracts.ColdStartFieldFieldLegalName), "Acme Robotics GmbH",
		siteProfileSnippetID(t, "Acme Robotics GmbH"))))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	req := trace.Requests[0]
	if !strings.Contains(req.System, "You extract a company's profile") {
		t.Errorf("the traced request is not the profile prompt: %q", req.System)
	}
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	content := req.Messages[0].Content
	if !strings.Contains(content, "<"+marker+">") || !strings.Contains(content, "</"+marker+">") {
		t.Errorf("the crawled pages are not wrapped in the declared boundary:\n%s", content)
	}
	instructions := outsideEverySpan(content, marker)
	for _, text := range []string{siteProfileHomeText, siteProfileImprintText, seedURL} {
		if strings.Contains(instructions, text) {
			t.Errorf("crawled text %q reaches the instruction region:\n%s", text, instructions)
		}
	}
	// The id enum is this call's index, not a constant: every id the gate can
	// resolve is offered, and nothing beyond it is.
	schemaJSON := string(req.ResponseSchema)
	for _, id := range []string{"s0", "s1"} {
		if !strings.Contains(schemaJSON, `"`+id+`"`) {
			t.Errorf("the schema does not offer %q, which this fixture's index carries: %s", id, schemaJSON)
		}
	}
	if strings.Contains(schemaJSON, `"s2"`) {
		t.Errorf("the schema offers an id this fixture's index does not carry: %s", schemaJSON)
	}
	if trace.Output == "" {
		t.Error("the trace records no model output for the gate to read")
	}
}

// An expectation the gate can never satisfy would measure nothing for as long as
// it stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestSiteProfileCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name       string
		want       map[string]string
		wantReason string
	}{
		{
			name:       "a field name the prompt never offers the model",
			want:       map[string]string{"favourite_colour": "blue"},
			wantReason: "never offers",
		},
		{
			name: "an empty value, which the gate drops from every reply",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldLegalName): "   ",
			},
			wantReason: "empty value",
		},
		{
			// The hard gate demands a verbatim-shaped value be IN the passage cited
			// for it, so a legal name no passage carries is refused whichever id the
			// model picks.
			name: "a verbatim-shaped value no passage of this crawl contains",
			want: map[string]string{
				string(crmcontracts.ColdStartFieldFieldLegalName): "Globex SE",
			},
			wantReason: "no passage of this fixture contains",
		},
		{
			name:       "no expectation at all",
			want:       map[string]string{},
			wantReason: "expects no field",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), siteProfileExpectation(t, tc.want))
			if err == nil {
				t.Fatalf("a scenario expecting %v prepared", tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say why it is unreachable: %v", err)
			}
		})
	}
}

// A paraphrase field is deliberately NOT held to containment: its overlap signal
// is warning-only, so the value a scenario expects may legitimately share no word
// with the passage that grounds it. Refusing one at Prepare would delete the
// cross-language reads this lane exists to admit.
func TestSiteProfileCaseAdmitsAParaphraseNoPassageContains(t *testing.T) {
	_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), siteProfileExpectation(t, map[string]string{
		string(crmcontracts.ColdStartFieldFieldIcp): "Fertigungsbetriebe in der DACH-Region",
	}))
	if err != nil {
		t.Errorf("a paraphrase expectation was refused: %v", err)
	}
}

// The deep read calls no model when the crawl yields no passage, so a scenario
// built on one would certify a request production never issues.
func TestSiteProfileCaseRefusesAFixtureThatWouldIssueNoCall(t *testing.T) {
	cases := []struct {
		name  string
		pages []siteProfilePage
	}{
		{name: "a crawl with no page at all", pages: nil},
		{
			name:  "pages whose text carries no passage",
			pages: []siteProfilePage{{URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome, Text: "   \n\n "}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := json.Marshal(siteProfileFixture{Pages: tc.pages})
			if err != nil {
				t.Fatalf("encoding the fixture: %v", err)
			}
			_, err = siteProfileCases{}.Prepare(fixture, siteProfileExpectation(t, siteProfileWantLegalName))
			if err == nil {
				t.Fatal("a crawl the profile lane would never send prepared")
			}
			if !strings.Contains(err.Error(), "no passage") {
				t.Errorf("the refusal does not say what the crawl lacks: %v", err)
			}
		})
	}
}

// A page kind the crawler never assigns describes a crawl the product cannot
// produce, and the kind is not decoration: it decides a page's excerpt budget and
// bounds the corpus to one legal page.
func TestSiteProfileCaseRefusesAPageKindTheCrawlerNeverAssigns(t *testing.T) {
	fixture, err := json.Marshal(siteProfileFixture{Pages: []siteProfilePage{
		{URL: seedURL, Kind: crmcontracts.SiteReadPageKind("careers"), Text: siteProfileHomeText},
	}})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	_, err = siteProfileCases{}.Prepare(fixture, siteProfileExpectation(t, siteProfileWantLegalName))
	if err == nil {
		t.Fatal("a page of an invented kind prepared")
	}
	if !strings.Contains(err.Error(), "careers") {
		t.Errorf("the refusal does not name the offending kind: %v", err)
	}
}

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestSiteProfileCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`["legal_name"]`), json.RawMessage(`7`)} {
		_, err := siteProfileCases{}.Prepare(siteProfileFixtureJSON(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "field to value") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheSiteProfileCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := siteProfileCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
