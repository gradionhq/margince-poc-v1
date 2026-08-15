// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The parallel extraction spine's contract, proven with a
// content-addressed fake (the fan-out is concurrent, so a scripted
// QUEUE would race — replies are keyed by which page a request reads
// instead): lanes merge, one page's failure degrades to partial without
// losing the rest, and the shared spine is what worker and CLI both run.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// laneFake answers by REQUEST CONTENT: the profile prompt gets the
// profile reply, a page prompt gets its page's reply — deterministic
// under any call order. failFor makes one page's call error.
type laneFake struct {
	profileReply string
	pageReplies  map[string]string // page URL → reply
	failFor      map[string]error  // page URL → error
	panicFor     map[string]bool   // page URL → panic instead of erroring
	panicProfile bool              // profile lane panics instead of replying
}

func (f laneFake) Complete(_ context.Context, req model.Request) (model.Response, error) {
	if strings.HasPrefix(req.System, profileSystem) {
		if f.panicProfile {
			panic("laneFake: profile lane panic")
		}
		return model.Response{Text: f.profileReply}, nil
	}
	if len(req.Messages) == 0 {
		return model.Response{}, errors.New("laneFake: no message")
	}
	content := req.Messages[0].Content
	for url := range f.panicFor {
		if strings.Contains(content, "url: "+url+"\n") {
			panic("laneFake: page " + url + " panic")
		}
	}
	for url, err := range f.failFor {
		if strings.Contains(content, "url: "+url+"\n") {
			return model.Response{}, err
		}
	}
	for url, reply := range f.pageReplies {
		if strings.Contains(content, "url: "+url+"\n") {
			return model.Response{Text: reply}, nil
		}
	}
	return model.Response{Text: `{"facts":[]}`}, nil
}

func extractFixturePages() []crawlPage {
	return []crawlPage{
		{
			URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome,
			Text: "Acme ships robots since 1998 and partners with SuperPLC across Europe for industrial automation lines.",
		},
		{
			URL: seedURL + "/impressum", Kind: crmcontracts.SiteReadPageKindImpressum,
			Text: "Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. Vertreten durch die Geschaeftsfuehrung.",
		},
		{
			URL: seedURL + "/services", Kind: crmcontracts.SiteReadPageKindServices,
			Text: "Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute and storage.",
		},
	}
}

func TestExtractSiteMergesTheParallelLanes(t *testing.T) {
	brain := laneFake{
		// Excerpt ids are global over the RANK-SORTED pages: the imprint
		// comes first, so s0 is its passage and s1 the home page's.
		profileReply: `{"fields":[
			{"f":"display_name","v":"Acme","e":"s1","c":0.9},
			{"f":"legal_name","v":"Acme Robotics GmbH","e":"s0","c":0.9}]}`,
		pageReplies: map[string]string{
			seedURL:                `{"facts":[{"f":"partner","v":"SuperPLC — automation platform","e":"s0"}]}`,
			seedURL + "/impressum": `{"facts":[],"entities":[{"n":"Acme Robotics GmbH","e":"s0"}]}`,
			seedURL + "/services":  `{"facts":[{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"}]}`,
		},
	}
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), nil)
	if got.err != nil {
		t.Fatalf("extractSite: %v", got.err)
	}
	if len(got.fields) != 2 {
		t.Fatalf("profile fields = %+v, want display_name + legal_name", got.fields)
	}
	if len(got.merged.facts) != 2 {
		t.Fatalf("facts = %+v, want the partner + the service", got.merged.facts)
	}
	if len(got.merged.entities) != 1 || got.merged.entities[0].Name != "Acme Robotics GmbH" {
		t.Fatalf("entities = %+v, want the imprint's census", got.merged.entities)
	}
	// The one-entity census lets the legal trio stand through the gate.
	fields, conflict, _ := applyLegalGate(got.fields, got.merged.entities, pageKindsOf(extractFixturePages()), false)
	if conflict || len(fields) != 2 {
		t.Fatalf("single-entity census must keep the trio: conflict=%v fields=%+v", conflict, fields)
	}
}

func TestExtractSiteOnePageFailureDegradesNotDiscards(t *testing.T) {
	brain := laneFake{
		profileReply: `{"fields":[{"f":"display_name","v":"Acme","e":"s0","c":0.9}]}`,
		pageReplies: map[string]string{
			seedURL + "/services": `{"facts":[{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"}]}`,
		},
		failFor: map[string]error{seedURL + "/impressum": errors.New("provider down")},
	}
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), nil)
	if got.err == nil || !strings.Contains(got.err.Error(), "/impressum") {
		t.Fatalf("the failed page must be reported: %v", got.err)
	}
	if len(got.merged.facts) != 1 {
		t.Fatalf("the surviving page's fact must be kept: %+v", got.merged.facts)
	}
	if len(got.fields) != 1 {
		t.Fatalf("the profile lane must survive a page failure: %+v", got.fields)
	}
}

// TestExtractSitePageFactPanicDegradesNotCrashes proves safeExtractPageFacts:
// a panic in one page's fact lane — a goroutine among up to
// pageExtractConcurrency siblings — must degrade like any other page
// failure, not take the whole process down with it.
func TestExtractSitePageFactPanicDegradesNotCrashes(t *testing.T) {
	brain := laneFake{
		profileReply: `{"fields":[{"f":"display_name","v":"Acme","e":"s0","c":0.9}]}`,
		pageReplies: map[string]string{
			seedURL + "/services": `{"facts":[{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"}]}`,
		},
		panicFor: map[string]bool{seedURL + "/impressum": true},
	}
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), nil)
	if got.err == nil || !strings.Contains(got.err.Error(), "panic") {
		t.Fatalf("the panicking page must surface as an ordinary error naming the panic: %v", got.err)
	}
	if len(got.merged.facts) != 1 {
		t.Fatalf("the surviving page's fact must be kept: %+v", got.merged.facts)
	}
	if len(got.fields) != 1 {
		t.Fatalf("the profile lane must survive a sibling page's panic: %+v", got.fields)
	}
}

// TestExtractSiteProfilePanicDegradesNotCrashes is
// TestExtractSitePageFactPanicDegradesNotCrashes' counterpart for the
// profile lane (safeExtractProfile): its panic must not crash the process
// either, and the page-fact lane it runs alongside must still complete.
func TestExtractSiteProfilePanicDegradesNotCrashes(t *testing.T) {
	brain := laneFake{
		panicProfile: true,
		pageReplies: map[string]string{
			seedURL + "/services": `{"facts":[{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"}]}`,
		},
	}
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), nil)
	if got.err == nil || !strings.Contains(got.err.Error(), "panic") {
		t.Fatalf("the panicking profile lane must surface as an ordinary error naming the panic: %v", got.err)
	}
	if got.fields != nil {
		t.Fatalf("a panicking profile lane must yield no fields, got %+v", got.fields)
	}
	if len(got.merged.facts) != 1 {
		t.Fatalf("the page-fact lane must survive the profile lane's panic: %+v", got.merged.facts)
	}
}

// streamFixtureSite builds a site of n fact-bearing pages (plus the
// seed) so the streaming spine can be driven through the real crawler
// against the in-memory fetcher.
func streamFixtureSite(n int) *fakeSite {
	site := &fakeSite{pages: seedOnly()}
	for i := 0; i < n; i++ {
		pageURL := fmt.Sprintf("%s/services-%02d", seedURL, i)
		site.sitemap = append(site.sitemap, pageURL)
		site.pages[pageURL] = fakeSitePage{text: fmt.Sprintf("Audit %02d\n", i) + readable(fmt.Sprintf("catalog %02d", i))}
	}
	return site
}

// The production streaming spine under -race: page calls launch per
// crawl commit, the profile lane fires EXACTLY once — via the
// page-count trigger on a large crawl and via the end-of-crawl fallback
// on a small one — and the merge is commit-ordered regardless of
// completion scheduling.
func TestCrawlAndExtractStreamsDeterministicallyAndProfilesTheWholeCrawl(t *testing.T) {
	// The first size crosses the trigger and then keeps crawling, so the
	// profile is asked again on the finished corpus; the second ends below
	// the trigger and is profiled once, on everything it has.
	for _, pages := range []int{profileTriggerNonLegalPages + 4, 3} {
		site := streamFixtureSite(pages)
		var profileCalls atomic.Int32
		brain := countingLaneFake{
			laneFake: laneFake{profileReply: `{"fields":[]}`, pageReplies: map[string]string{}},
			profile:  &profileCalls,
		}
		for i := 0; i < pages; i++ {
			pageURL := fmt.Sprintf("%s/services-%02d", seedURL, i)
			brain.pageReplies[pageURL] = fmt.Sprintf(`{"facts":[{"f":"service","v":"Audit %02d — catalog line","e":"s0"}]}`, i)
		}
		crawler := testSiteCrawler(site)
		crawler.fetchWave = crawler.maxPages // one wave: every page commits in the same round
		var published []int
		var progressPages []crawlPage
		crawl, extraction, err := crawlAndExtract(context.Background(), crawler,
			evidenceExtractor{brain: brain, factBrain: brain}, seedURL, func(_ string, committed []crawlPage) {
				progressPages = append([]crawlPage(nil), committed...)
			}, func(partial pageFactsResult) {
				published = append(published, len(partial.facts))
			})
		if err != nil {
			t.Fatalf("crawlAndExtract(%d pages): %v", pages, err)
		}
		if extraction.err != nil {
			t.Fatalf("clean lanes reported an error: %v", extraction.err)
		}
		wantProfileCalls := int32(1)
		if pages >= profileTriggerNonLegalPages*profileGrowthFactor {
			// The crawl at least doubled after the trigger, so the early
			// answer was drawn from a fraction of the evidence and is asked
			// again over the whole of it.
			wantProfileCalls = 2
		}
		if got := profileCalls.Load(); got != wantProfileCalls {
			t.Fatalf("profile lane fired %d times for %d pages, want %d",
				got, pages, wantProfileCalls)
		}
		if len(extraction.merged.facts) != pages {
			t.Fatalf("facts = %d, want one per catalog page (%d)", len(extraction.merged.facts), pages)
		}
		if len(progressPages) != len(crawl.Pages) || progressPages[len(progressPages)-1].URL != crawl.Pages[len(crawl.Pages)-1].URL {
			t.Fatalf("live pages = %v, terminal pages = %v", progressPages, crawl.Pages)
		}
		if len(published) != pages {
			t.Fatalf("progressive drafts = %v, want %d snapshots", published, pages)
		}
		for i, got := range published {
			if got != i+1 {
				t.Fatalf("progressive drafts = %v, want counts 1..%d", published, pages)
			}
		}
		// Commit-ordered merge: fact order follows the crawl's page order,
		// whatever order the concurrent calls completed in.
		wantOrder := map[string]int{}
		rank := 0
		for _, page := range crawl.Pages {
			if page.Kind == crmcontracts.SiteReadPageKindServices {
				wantOrder[page.URL] = rank
				rank++
			}
		}
		for i, fact := range extraction.merged.facts {
			if wantOrder[fact.SourceURL] != i {
				t.Fatalf("fact %d came from %s — the merge is not commit-ordered: %+v", i, fact.SourceURL, extraction.merged.facts)
			}
		}
	}
}

// countingLaneFake counts profile-lane invocations on top of laneFake.
type countingLaneFake struct {
	laneFake
	profile *atomic.Int32
}

func (f countingLaneFake) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if strings.HasPrefix(req.System, profileSystem) {
		f.profile.Add(1)
	}
	return f.laneFake.Complete(ctx, req)
}

func TestFailedLegalPageWithholdsTheLegalTrio(t *testing.T) {
	// The imprint's fact call dies: its entities never voted, so even a
	// single-entity census cannot be trusted — the legal trio is
	// withheld with its own drop reason.
	brain := laneFake{
		profileReply: `{"fields":[
			{"f":"display_name","v":"Acme","e":"s1","c":0.9},
			{"f":"legal_name","v":"Acme Robotics GmbH","e":"s0","c":0.9}]}`,
		failFor: map[string]error{seedURL + "/impressum": errors.New("provider down")},
	}
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), nil)
	if !got.legalCensusIncomplete {
		t.Fatal("a failed legal page must mark the census incomplete")
	}
	fields, abstained, dropped := applyLegalGate(got.fields, got.merged.entities, pageKindsOf(extractFixturePages()), got.legalCensusIncomplete)
	if !abstained {
		t.Fatal("an incomplete census must abstain from the legal trio")
	}
	for _, f := range fields {
		if f.Field == "legal_name" {
			t.Fatalf("the trio leaked through an incomplete census: %+v", fields)
		}
	}
	found := false
	for _, d := range dropped {
		if d.Field == "legal_name" && d.Reason == dropLegalCensusIncomplete {
			found = true
		}
	}
	if !found {
		t.Fatalf("the withheld trio must carry legal_census_incomplete: %+v", dropped)
	}
}

func TestExtractSiteProgressCountsEveryPage(t *testing.T) {
	brain := laneFake{profileReply: `{"fields":[]}`}
	var maxDone int
	got := extractSite(context.Background(), evidenceExtractor{brain: brain, factBrain: brain}, extractFixturePages(), func(done int) {
		if done > maxDone {
			maxDone = done
		}
	})
	if got.err != nil {
		t.Fatalf("extractSite: %v", got.err)
	}
	if maxDone != len(extractFixturePages()) {
		t.Fatalf("progress reached %d, want every page (%d)", maxDone, len(extractFixturePages()))
	}
}

// growingProfileFake answers the profile lane by how much evidence it was
// shown: a prompt carrying the later pages yields the richer profile. That
// is the real asymmetry -- the About and team pages arrive after the
// trigger, so the first call cannot see them.
type growingProfileFake struct {
	laneFake
	profile *atomic.Int32
}

func (f growingProfileFake) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if strings.HasPrefix(req.System, profileSystem) {
		f.profile.Add(1)
		if strings.Contains(req.Messages[0].Content, "Audit 20") {
			// The whole crawl is in the prompt. This pass grounds fields the
			// early one could not see -- and DELIBERATELY not `industry`, so
			// a merge that dropped the first pass's work would be visible.
			return model.Response{Text: `{"fields":[` +
				`{"f":"display_name","v":"Acme","e":"s0","c":0.9},` +
				`{"f":"usp","v":"Audit 20","e":"s0","c":0.9}]}`}, nil
		}
		// The early pass sees only the first pages, and grounds `industry`.
		return model.Response{Text: `{"fields":[{"f":"industry","v":"Audit 00","e":"s0","c":0.9}]}`}, nil
	}
	return f.laneFake.Complete(ctx, req)
}

func TestALargeCrawlIsProfiledOnEverythingItFound(t *testing.T) {
	// A 40-page site was profiled from the first 8 pages and never asked
	// again, so every page the crawl found afterwards -- About, team,
	// services -- reached the fact lane but never the profile.
	const pages = profileTriggerNonLegalPages * 3
	site := streamFixtureSite(pages)
	var profileCalls atomic.Int32
	brain := growingProfileFake{
		laneFake: laneFake{pageReplies: map[string]string{}},
		profile:  &profileCalls,
	}
	for i := 0; i < pages; i++ {
		pageURL := fmt.Sprintf("%s/services-%02d", seedURL, i)
		brain.pageReplies[pageURL] = fmt.Sprintf(
			`{"facts":[{"f":"service","v":"Audit %02d — catalog line","e":"s0"}]}`, i)
	}
	crawler := testSiteCrawler(site)
	// Commit a few pages per round, the way a real crawl arrives. With the
	// whole site landing in one wave the trigger already sees everything
	// and a second pass would be pointless -- which is itself correct, and
	// is what the sibling test covers.
	crawler.fetchWave = 4

	_, extraction, err := crawlAndExtract(context.Background(), crawler,
		evidenceExtractor{brain: brain, factBrain: brain}, seedURL, nil, nil)
	if err != nil {
		t.Fatalf("crawlAndExtract: %v", err)
	}
	if extraction.err != nil {
		t.Fatalf("clean lanes reported an error: %v", extraction.err)
	}
	if got := profileCalls.Load(); got != 2 {
		t.Fatalf("profile lane fired %d times, want 2 (early answer, then the whole crawl)", got)
	}
	// Assert by NAME, not by count: a count is satisfied by the re-run's
	// output alone, so it stays green even if the merge is deleted and the
	// first pass's work is thrown away.
	grounded := map[string]bool{}
	for _, field := range extraction.fields {
		grounded[field.Field] = true
	}
	for _, want := range []string{"industry", "display_name", "usp"} {
		if !grounded[want] {
			t.Errorf("%q is missing; the read kept %v", want, grounded)
		}
	}
}

func TestARerunNeverDiscardsAFieldTheFirstPassGrounded(t *testing.T) {
	// The two passes read different evidence, so the second can ground
	// fields the first missed while missing one the first had. Choosing the
	// LONGER list would drop that field silently -- both answers went
	// through the same gate, so a field either pass grounded is a field the
	// site supports.
	first := []evidencedField{
		{Field: "industry", Value: "Retail software"},
		{Field: "display_name", Value: "Acme"},
	}
	second := []evidencedField{
		{Field: "display_name", Value: "Acme SE"},
		{Field: "usp", Value: "Fastest onboarding in the segment"},
	}

	merged := mergeProfileFields(first, second)

	got := map[string]string{}
	for _, field := range merged {
		got[field.Field] = field.Value
	}
	if _, kept := got["industry"]; !kept {
		t.Errorf("the re-run dropped `industry`, which the first pass grounded: %v", got)
	}
	if _, added := got["usp"]; !added {
		t.Errorf("the merge lost `usp`, which only the re-run found: %v", got)
	}
	// The re-run read the whole crawl, so its value for a contested field is
	// the better grounded one.
	if got["display_name"] != "Acme SE" {
		t.Errorf("display_name = %q, want the re-run's value", got["display_name"])
	}
	if len(merged) != 3 {
		t.Errorf("merged %d fields, want 3 distinct ones: %v", len(merged), got)
	}
}

// budgetDeferringFake fails the FIRST profile call the way an over-budget
// workspace does, and answers the second normally.
type budgetDeferringFake struct {
	laneFake
	calls *atomic.Int32
}

func (f budgetDeferringFake) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if strings.HasPrefix(req.System, profileSystem) {
		if f.calls.Add(1) == 1 {
			return model.Response{}, &ai.BudgetDeferralError{}
		}
		return model.Response{Text: `{"fields":[{"f":"industry","v":"Audit 00","e":"s0","c":0.9}]}`}, nil
	}
	return f.laneFake.Complete(ctx, req)
}

func TestAnOverBudgetFirstPassIsNotAskedAgainWithABiggerPrompt(t *testing.T) {
	// A budget deferral means the workspace is over its token threshold.
	// Asking again over the WHOLE crawl spends more against an allowance
	// that is already exhausted, and defers a second time.
	const pages = profileTriggerNonLegalPages * 3
	site := streamFixtureSite(pages)
	var calls atomic.Int32
	brain := budgetDeferringFake{
		laneFake: laneFake{pageReplies: map[string]string{}},
		calls:    &calls,
	}
	for i := 0; i < pages; i++ {
		brain.pageReplies[fmt.Sprintf("%s/services-%02d", seedURL, i)] = `{"facts":[]}`
	}
	crawler := testSiteCrawler(site)
	crawler.fetchWave = 4

	_, extraction, err := crawlAndExtract(context.Background(), crawler,
		evidenceExtractor{brain: brain, factBrain: brain}, seedURL, nil, nil)
	if err != nil {
		t.Fatalf("crawlAndExtract: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("profile lane called %d times after a budget deferral, want 1", got)
	}
	// The deferral must still reach the caller: it is how the read is
	// re-queued rather than shipped half-done.
	if extraction.err == nil {
		t.Error("the budget deferral was swallowed; the read cannot be re-queued")
	}
}

func TestARerunThatRescuesAFailedFirstPassLeavesTheReadWhole(t *testing.T) {
	// The first pass failed and the re-run answered, so the lane produced
	// its profile. Reporting the read partial would warn a user about
	// nothing missing.
	const pages = profileTriggerNonLegalPages * 3
	site := streamFixtureSite(pages)
	var calls atomic.Int32
	brain := failThenAnswerFake{
		laneFake: laneFake{pageReplies: map[string]string{}},
		calls:    &calls,
	}
	for i := 0; i < pages; i++ {
		brain.pageReplies[fmt.Sprintf("%s/services-%02d", seedURL, i)] = `{"facts":[]}`
	}
	crawler := testSiteCrawler(site)
	crawler.fetchWave = 4

	_, extraction, err := crawlAndExtract(context.Background(), crawler,
		evidenceExtractor{brain: brain, factBrain: brain}, seedURL, nil, nil)
	if err != nil {
		t.Fatalf("crawlAndExtract: %v", err)
	}
	if len(extraction.fields) == 0 {
		t.Fatal("the re-run's profile was lost")
	}
	if extraction.err != nil {
		t.Errorf("the read reports an error though the re-run answered: %v", extraction.err)
	}
}

// failThenAnswerFake fails the first profile call with an ordinary provider
// error -- not a budget deferral -- and answers the second.
type failThenAnswerFake struct {
	laneFake
	calls *atomic.Int32
}

func (f failThenAnswerFake) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if strings.HasPrefix(req.System, profileSystem) {
		if f.calls.Add(1) == 1 {
			return model.Response{}, errors.New("provider unavailable")
		}
		return model.Response{Text: `{"fields":[{"f":"industry","v":"Audit 00","e":"s0","c":0.9}]}`}, nil
	}
	return f.laneFake.Complete(ctx, req)
}
