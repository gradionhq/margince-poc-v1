// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for site_fact_extract/page_facts.
//
// It certifies the shipped path rather than a description of it: the menu comes
// from menuForKind, the numbering from newSnippetIndex, the request from
// pageFactsRequest and the verdict from gatePageFacts — the same four steps one
// page of the deep read takes, in the same order. A case that rebuilt any of
// them would measure a copy, and a copy stays green through the change that
// breaks the original.
//
// This is the most per-call site in the census, and it varies along two axes at
// once. The page's KIND selects the menu, and the menu is half the prompt and
// half the schema together: which lanes the model is asked for, and which fact
// fields its enum may answer with. The numbered index decides which passages the
// model is shown, which ids the schema offers, and which text the gate resolves a
// citation back into. Both are rebuilt from the fixture exactly as production
// rebuilds them per page — pinning either would certify a call the product never
// makes, on a page kind it never makes it for.
//
// The lane the production call runs on is deliberately not part of the fixture.
// The fact lane is served by its own brain when one is injected and by the main
// one otherwise, but that choice happens AFTER the request is built and does not
// change a byte of it; which model answered is the record's binding axis, named
// there rather than asserted here.
//
// What the expectation MEANS here: the facts this page must ground, as field to
// value. Facts are what this lane is named for and what the deep read stores as
// organization facts, and their vocabulary is the closed menu the schema enum
// offers — which is what lets an unanswerable expectation be named at Prepare
// instead of measured as a zero. The same call carries the people and entity
// lanes for the kinds whose menu asks for them, and every refusal in those lanes
// reaches the Detail, so a fabricated person or a sibling entity's address shows
// up in the record.
//
// It is a subset claim, never an inventory: one page grounds more than a
// scenario cares to pin, and demanding exhaustiveness would fail a read for being
// richer than its author imagined.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// sitePageFactsFixture is ONE crawled page in exactly the three fields that
// reach the prompt or the gate. The kind is not decoration — it selects the
// menu, and the menu decides what this call asks for at all. A crawled page also
// carries its byte count and fetch duration, and those reach the debug report
// alone: a fixture carrying them would describe a crawl rather than a prompt.
type sitePageFactsFixture struct {
	URL  string                        `json:"url"`
	Kind crmcontracts.SiteReadPageKind `json:"kind"`
	Text string                        `json:"text"`
}

// sitePageFactsCases serves the site that reads one page of a company's website
// for the facts that page states.
type sitePageFactsCases struct{}

func (sitePageFactsCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskSiteFactExtract,
		Variant: "page_facts",
		Kind:    ai.SiteKindOneShot,
	}
}

// CertifiedScope narrows the record to the one page this case reads. A deep read
// issues this call once per crawled page and the read's answer is the merge of
// all of them: page kind decides which of two conflicting facts wins, duplicates
// collapse, and the band quota cuts the tail. None of that exists for one page,
// so a run measures one page's facts and not the fact set a human is shown.
func (sitePageFactsCases) CertifiedScope() string { return aitasks.ScopeSingleCall }

// Prepare turns one crawled page and the facts the scenario expects from it into
// a runnable case, routing the kind to its menu and numbering the passages
// exactly as the fact lane does, so the prompt this case sends and the schema it
// offers are the ones that page would actually be read with.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (sitePageFactsCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f sitePageFactsFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("site_fact_extract/page_facts: the fixture is not the shape this site takes: %w", err)
	}
	if !f.Kind.Valid() {
		return nil, fmt.Errorf(
			"site_fact_extract/page_facts: the page is of kind %q, which the crawler never classifies a page as", f.Kind)
	}
	menu, routed := menuForKind(f.Kind)
	if !routed {
		return nil, fmt.Errorf(
			"site_fact_extract/page_facts: a page of kind %q states too few facts to be worth a call, so the lane makes "+
				"none — and a scenario over one would certify a request the product never issues", f.Kind)
	}
	page := crawlPage{URL: f.URL, Kind: f.Kind, Text: f.Text}
	idx := newSnippetIndex([]crawlPage{page})
	if len(idx.refs) == 0 {
		return nil, errors.New(
			"site_fact_extract/page_facts: the fixture's page yields no passage, and the lane calls no model without one")
	}
	var want map[string]string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("site_fact_extract/page_facts: the expected answer is not a field to value map: %w", err)
	}
	if len(want) == 0 {
		return nil, errors.New("site_fact_extract/page_facts: the scenario expects no fact, so no reply could disagree with it")
	}
	if err := refuseUngroundableFacts(want, f.Kind, menu, idx); err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(want))
	for field, value := range want {
		identities[field] = factIdentity(field, value)
	}
	return &sitePageFactsCase{page: page, menu: menu, idx: idx, expected: identities}, nil
}

// refuseUngroundableFacts names an expectation this page could never answer: a
// field outside the menu its kind carries is one the schema enum does not offer
// and the gate drops as unknown on every reply, an empty value is dropped as
// empty on every reply, a measured zero is dropped as a pre-animation figure, and
// a value whose name no passage of this page carries is refused by the citation
// gate whichever passage the model cites. Each would measure nothing for as long
// as it stayed in the corpus. Naming it here costs a parse; finding it later
// costs a paid run.
//
// The containment check reads the index rather than the fixture's raw text,
// because a passage is what a citation resolves to — and it asks for the value's
// NAME, which is what the gate demands of a list field whose value carries a
// description the page supplied.
//
// Sorted so a fixture with two offences names the same one every time.
func refuseUngroundableFacts(want map[string]string, kind crmcontracts.SiteReadPageKind, menu pageMenu, idx snippetIndex) error {
	for _, field := range slices.Sorted(maps.Keys(want)) {
		value := want[field]
		switch {
		case !slices.Contains(menu.factFields, field):
			return fmt.Errorf(
				"site_fact_extract/page_facts: the scenario expects %q, which a %s page's menu never offers the model",
				field, kind)
		case strings.TrimSpace(value) == "":
			return fmt.Errorf(
				"site_fact_extract/page_facts: the scenario expects an empty value for %q, which the gate drops", field)
		case zeroedStat(field, value):
			return fmt.Errorf(
				"site_fact_extract/page_facts: the scenario expects %q to read %q, which the gate drops as a figure a "+
					"page animated up from zero", field, value)
		case !citableInSomePassage(idx, factName(value)):
			return fmt.Errorf(
				"site_fact_extract/page_facts: the scenario expects %q to read %q, whose name no passage of this fixture "+
					"contains, and the gate demands it appear in the passage cited for it", field, value)
		}
	}
	return nil
}

// factIdentity is the part of a fact's value a scenario can hold it to. A
// multi-value field is spelled "Name — short description", the PAGE decides
// whether a description exists at all, and the gate itself dedupes on the name —
// so the name is the claim, and a description its author could not have predicted
// does not decide the verdict. Every other field states one value and is held to
// all of it.
func factIdentity(field, value string) string {
	if people.OrganizationFactMultiValue[field] {
		return factName(value)
	}
	return strings.TrimSpace(value)
}

// sitePageFactsCase is one page ready to be read, closed over the menu its kind
// routes to, the numbered index the prompt, the schema and the gate all share,
// and the facts the scenario expects.
type sitePageFactsCase struct {
	page     crawlPage
	menu     pageMenu
	idx      snippetIndex
	expected map[string]string
}

// Run issues the one request this page sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told to try
// again rather than the answer it gives.
func (c *sitePageFactsCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := pageFactsRequest(c.menu, c.idx)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("site_fact_extract/page_facts: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the no-guess gate against the SAME page, menu and index the
// model was shown, and only then asks whether what survived is what the scenario
// expects. The order is the meaning: a fact the gate refused is not a fact to
// disagree with.
//
// A reply is unusable when the fact lane refused everything it claimed there: an
// unreadable answer, a field off this page's menu, a value the cited passage does
// not name. Claiming NOTHING — no fact, no person, no entity, and nothing for
// any lane to refuse — is the opposite event and is reported as an abstention,
// because omission is what this prompt asks for when the page states nothing:
// the lane stores no fact and the deep read carries on, exactly as it does after
// a page that grounded ten.
//
// The abstention is asked of every lane the call carries, not of the facts
// alone. A reply that named three people states plenty; that the scenario grades
// facts is a fact about the scenario, and calling such a reply silent would put
// the word "abstained" on a record for a model that spoke.
//
// Nothing is imposed beyond the gate: the fact lane has no acceptance floor of
// its own past the citation rule, so a case that added one would refuse a fact
// the product keeps.
func (c *sitePageFactsCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	result, dropped := gatePageFacts(trace.Output, c.page, c.menu, c.idx)
	// Every refusal reaches the Detail whatever the result, the people and entity
	// lanes included: a reply that grounded the expected facts while inventing a
	// person is not the clean run it would otherwise look like.
	detail := gateRefusals(dropped)
	if len(result.facts) == 0 && factLaneRefused(dropped) {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	grounded := groundedFactIdentities(result.facts, c.expected)
	disagreements := expectationDisagreements(c.expected, grounded)
	if pageFactsReplyIsSilent(result, dropped) {
		return aitasks.Outcome{
			Result: aitasks.OutcomeAbstained,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	if len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}

// pageFactsReplyIsSilent answers whether this reply claimed nothing at all: no
// lane grounded a row and no lane refused one. The refusals matter as much as
// the rows — a reply whose every claim the gate dropped said something, and
// scoring it as silence would credit a fabricator with the restraint of a model
// that declined to fabricate.
func pageFactsReplyIsSilent(result pageFactsResult, dropped []droppedFinding) bool {
	return len(result.facts) == 0 &&
		len(result.people) == 0 &&
		len(result.entities) == 0 &&
		len(dropped) == 0
}

// factLaneRefused answers whether the FACT lane refused anything, which is what
// separates a reply the gate emptied from one that stated no fact. A refusal in
// the people or entity lane says nothing about either.
func factLaneRefused(dropped []droppedFinding) bool {
	for _, d := range dropped {
		if d.Lane == lanePageFacts {
			return true
		}
	}
	return false
}

// groundedFactIdentities keys one page's surviving facts by field for the shared
// comparison. Most fact fields are MULTI-value — every offering, market and
// signal field, plus location — so one page legitimately grounds several rows
// under one name while a scenario names one of them. The row the scenario names
// wins where the reply carries it; otherwise the first grounded row stands, so a
// disagreement still reports what the page did say instead of an absence.
func groundedFactIdentities(facts []people.DeepReadFact, expected map[string]string) map[string]string {
	out := make(map[string]string, len(facts))
	for _, f := range facts {
		identity := factIdentity(f.Field, f.Value)
		current, seen := out[f.Field]
		switch {
		case !seen:
			out[f.Field] = identity
		case normalizeEvidence(current) == normalizeEvidence(expected[f.Field]):
			// The row the scenario names already stands.
		case normalizeEvidence(identity) == normalizeEvidence(expected[f.Field]):
			out[f.Field] = identity
		}
	}
	return out
}
