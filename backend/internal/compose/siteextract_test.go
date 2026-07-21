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
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// laneFake answers by REQUEST CONTENT: the profile prompt gets the
// profile reply, a page prompt gets its page's reply — deterministic
// under any call order. failFor makes one page's call error.
type laneFake struct {
	profileReply string
	pageReplies  map[string]string // page URL → reply
	failFor      map[string]error  // page URL → error
}

func (f laneFake) Complete(_ context.Context, req model.Request) (model.Response, error) {
	if req.System == profileSystem {
		return model.Response{Text: f.profileReply}, nil
	}
	if len(req.Messages) == 0 {
		return model.Response{}, errors.New("laneFake: no message")
	}
	content := req.Messages[0].Content
	for url, err := range f.failFor {
		if strings.HasPrefix(content, "Page "+url+":") {
			return model.Response{}, err
		}
	}
	for url, reply := range f.pageReplies {
		if strings.HasPrefix(content, "Page "+url+":") {
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
func TestCrawlAndExtractStreamsDeterministicallyAndFiresProfileOnce(t *testing.T) {
	for _, pages := range []int{profileTriggerPages + 4, 3} {
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
		crawl, extraction, err := crawlAndExtract(context.Background(), crawler,
			evidenceExtractor{brain: brain, factBrain: brain}, seedURL, nil, func(partial pageFactsResult) {
				published = append(published, len(partial.facts))
			})
		if err != nil {
			t.Fatalf("crawlAndExtract(%d pages): %v", pages, err)
		}
		if extraction.err != nil {
			t.Fatalf("clean lanes reported an error: %v", extraction.err)
		}
		if got := profileCalls.Load(); got != 1 {
			t.Fatalf("profile lane fired %d times for %d pages, want exactly once", got, pages)
		}
		if len(extraction.merged.facts) != pages {
			t.Fatalf("facts = %d, want one per catalog page (%d)", len(extraction.merged.facts), pages)
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
	if req.System == profileSystem {
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
