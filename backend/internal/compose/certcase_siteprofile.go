// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for site_extract/profile.
//
// It certifies the shipped path rather than a description of it: the excerpt
// corpus comes from profileExcerptPages, the numbering from newSnippetIndex, the
// request from profileRequest and the verdict from gateProfile — the same four
// steps the deep read takes, in the same order. A case that rebuilt any of them
// would measure a copy, and a copy stays green through the change that breaks
// the original.
//
// The numbered index is the sharp edge here, because it decides three things at
// once: which passages the model is shown, which ids this call's response schema
// offers, and which text the citation gate resolves an id back into. So the index
// is built once, in Prepare, and the request and the gate both take it. A case
// that pinned a schema, or numbered the passages a second time, would let the
// model cite an id the gate reads as some other passage — and certify a mismatch
// as a clean read.
//
// What the expectation MEANS here: the profile fields that must survive the
// citation gate, with the values they must carry. The lane grounds eleven company
// fields against cited passages, so what it is right or wrong about is a field —
// it either grounds the one the scenario named, or it grounds something else, or
// it grounds nothing. It is a subset claim, never an inventory: a real site
// grounds more of the eleven than a scenario cares to pin, and demanding
// exhaustiveness would fail a read for being richer than its author imagined.

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
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// siteProfileFixture is ONE crawl in the only thing the profile lane reads from
// it: its pages.
type siteProfileFixture struct {
	Pages []siteProfilePage `json:"pages"`
}

// siteProfilePage is one crawled page in exactly the three fields that reach the
// model or the gate. The kind is not decoration — it decides a page's share of
// the excerpt budget and bounds the corpus to one legal page. A crawled page also
// carries its byte count and fetch duration, and those reach the debug report
// alone: a fixture carrying them would describe a crawl rather than a prompt.
type siteProfilePage struct {
	URL  string                        `json:"url"`
	Kind crmcontracts.SiteReadPageKind `json:"kind"`
	Text string                        `json:"text"`
}

// siteProfileCases serves the one site that grounds a company's profile in the
// passages of its own website.
type siteProfileCases struct{}

func (siteProfileCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskSiteExtract,
		Variant: "profile",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one crawl and the fields the scenario expects from it into a
// runnable case, selecting and numbering the excerpts exactly as the deep read
// does so the schema this case offers enumerates the same ids the gate this case
// runs can resolve.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (siteProfileCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f siteProfileFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("site_extract/profile: the fixture is not the shape this site takes: %w", err)
	}
	pages := make([]crawlPage, 0, len(f.Pages))
	for i, page := range f.Pages {
		if !page.Kind.Valid() {
			return nil, fmt.Errorf(
				"site_extract/profile: page %d is of kind %q, which the crawler never classifies a page as",
				i+1, page.Kind)
		}
		pages = append(pages, crawlPage{URL: page.URL, Kind: page.Kind, Text: page.Text})
	}
	// The deep read's own two steps, in its own order: the excerpt corpus is
	// selected under the profile budget, then numbered once — for the prompt, the
	// schema and the gate alike.
	idx := newSnippetIndex(profileExcerptPages(pages))
	if len(idx.refs) == 0 {
		return nil, errors.New(
			"site_extract/profile: the fixture's pages yield no passage, and the deep read calls no model without one")
	}
	var want map[string]string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("site_extract/profile: the expected answer is not a field to value map: %w", err)
	}
	if len(want) == 0 {
		return nil, errors.New("site_extract/profile: the scenario expects no field, so no reply could disagree with it")
	}
	if err := refuseUngroundableExpectation(want, idx); err != nil {
		return nil, err
	}
	return &siteProfileCase{idx: idx, expected: want}, nil
}

// refuseUngroundableExpectation names an expectation this site could never
// answer: a field outside the vocabulary the prompt offers is one no model was
// told exists and the gate drops as unknown on every reply, an empty value is
// dropped as empty on every reply, and a verbatim-shaped value that no passage of
// this crawl carries is refused by the hard gate whichever passage the model
// cites. Each would measure nothing for as long as it stayed in the corpus.
// Naming it here costs a parse; finding it later costs a paid run.
//
// The containment check reads the index rather than the fixture's raw text,
// because the excerpt selection caps every page: a value the budget cut is one
// the model will not be shown, and the refusal has to speak about the corpus this
// call actually sends.
//
// A paraphrase field is deliberately not checked against the passages. Its
// overlap signal is warning-only — a German passage rendered into an English
// value shares nothing lexically and the lane wants that value — so any wording
// the crawl grounds in substance can survive, and refusing one here would delete
// the cross-language reads the warning class exists to admit.
//
// Sorted so a fixture with two offences names the same one every time.
func refuseUngroundableExpectation(want map[string]string, idx snippetIndex) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		value := want[name]
		switch {
		case !slices.Contains(extractionFieldNames, name):
			return fmt.Errorf(
				"site_extract/profile: the scenario expects %q, which this prompt never offers the model", name)
		case strings.TrimSpace(value) == "":
			return fmt.Errorf(
				"site_extract/profile: the scenario expects an empty value for %q, which the gate drops", name)
		case hardGateProfileFields[name] && !citableInSomePassage(idx, value):
			return fmt.Errorf(
				"site_extract/profile: the scenario expects %q to read %q, which no passage of this fixture contains, "+
					"and the gate demands a verbatim-shaped value appear in the passage cited for it", name, value)
		}
	}
	return nil
}

// citableInSomePassage answers whether ANY passage of this call's index would
// satisfy the hard gate for value, under the gate's own containment rule —
// including the one-passage boundary forgiveness, so a value split across two
// adjacent passages of the same page counts here exactly as it would at run time.
func citableInSomePassage(idx snippetIndex, value string) bool {
	for _, id := range idx.ids() {
		if _, cited := idx.nameInCited(id, value); cited {
			return true
		}
	}
	return false
}

// siteProfileCase is one crawl ready to be read, closed over the numbered index
// the prompt, the schema and the gate all share, and the fields the scenario
// expects.
type siteProfileCase struct {
	idx      snippetIndex
	expected map[string]string
}

// Run issues the one request this site sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told to try
// again rather than the answer it gives.
func (c *siteProfileCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := profileRequest(c.idx)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("site_extract/profile: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the citation gate against the SAME index the model was shown
// and only then asks whether what survived is what the scenario expects. The
// order is the meaning: a field the gate refused is not a field to disagree with.
//
// A reply is unusable only when the gate refused everything it claimed: an
// unreadable answer, an id outside this call's index, a legal name the imprint
// never carries. Claiming NOTHING is a different thing and is judged a wrong
// answer, because omission is what this prompt asks for when the passages ground
// nothing — the lane stores no profile field and the deep read carries on.
//
// Nothing is imposed beyond the gate: the lane has no acceptance floor of its own
// past the confidence range the gate already enforces, so a case that added one
// would refuse a field the product keeps.
func (c *siteProfileCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	grounded, dropped := gateProfile(trace.Output, c.idx)
	// Every refusal reaches the Detail whatever the result — the warning-class
	// overlap included: a reply that grounded the expected field while paraphrasing
	// three others away from their citations is not the clean run it would
	// otherwise look like.
	detail := gateRefusals(dropped)
	if len(grounded) == 0 && len(dropped) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	if disagreements := expectationDisagreements(c.expected, groundedValues(grounded)); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}
