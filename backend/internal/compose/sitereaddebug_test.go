// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The debug facade's contract: it runs the worker's exact
// crawl→corpus→gate spine and reports every intermediate — pages,
// call telemetry, drops, the legal-entity census, and the byte-identical
// proposal payload — without a database or staging.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestSiteReadDebugReportsPagesCorpusCallAndProposal(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Acme home.") + " Onboard your team in minutes, not weeks."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart."},
	}}
	// ONE corpus reply covers the whole site: a field off each page, one
	// fact, and the single-entity census that lets the legal trio stand.
	fake := ai.NewFakeClient().Script(`{"fields":[
			{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","source_url":"` + seedURL + `","confidence":0.9},
			{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","source_url":"` + seedURL + `/impressum","confidence":0.9}],
		"facts":[
			{"category":"company","field":"location","value":"Stuttgart","evidence_snippet":"70435 Stuttgart","source_url":"` + seedURL + `/impressum","confidence":0.9}],
		"people":[],
		"legal_entities":[{"name":"Acme Robotics GmbH","source_url":"` + seedURL + `/impressum"}]}`)

	var phases []string
	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{
			SeedURL: seedURL, Brain: fakeModelPath(t, fake).SiteExtract, IncludePageText: true,
			Progress: func(phase string, done, total int) { phases = append(phases, phase) },
		},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}

	if got := len(report.Crawl.Pages); got != 2 {
		t.Fatalf("reported %d pages, want 2 (home + impressum): %+v", got, report.Crawl.Pages)
	}
	for _, page := range report.Crawl.Pages {
		if !page.Extracted {
			t.Fatalf("every page was in the one extracted chunk: %+v", report.Crawl.Pages)
		}
	}
	if report.Crawl.Pages[0].Text == "" {
		t.Fatal("IncludePageText was set but the page text is missing from the report")
	}
	if len(phases) < 2 {
		t.Fatalf("progress must fire for the crawl and the chunk: %v", phases)
	}

	byName := map[string]DebugField{}
	for _, f := range report.Extraction.Fields {
		byName[f.Field] = f
	}
	if byName["legal_name"].Value != "Acme Robotics GmbH" {
		t.Fatalf("the single-entity legal trio must stand: %+v", report.Extraction.Fields)
	}
	if len(report.Extraction.Facts) != 1 || report.Extraction.Facts[0].Field != "location" {
		t.Fatalf("the location fact is missing: %+v", report.Extraction.Facts)
	}
	if len(report.Extraction.LegalEntities) != 1 {
		t.Fatalf("the census must be reported: %+v", report.Extraction.LegalEntities)
	}

	if got := len(report.ModelCalls); got != 1 {
		t.Fatalf("recorded %d model calls, want the ONE corpus call: %+v", got, report.ModelCalls)
	}
	if report.ModelCalls[0].Lane != laneCorpus {
		t.Fatalf("call lane = %s, want corpus", report.ModelCalls[0].Lane)
	}

	if report.Proposal == nil {
		t.Fatal("findings survived but the report carries no proposal payload")
	}
	if len(report.Proposal.Fields) != len(report.Extraction.Fields) || len(report.Proposal.Facts) != len(report.Extraction.Facts) {
		t.Fatalf("the proposal payload disagrees with the reported extraction: %+v vs %+v",
			report.Proposal, report.Extraction)
	}
}

func TestSiteReadDebugGateEmptyReplyReportsCleanWithNoLaneError(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL: {text: readable("Acme home.")},
	}}
	fake := ai.NewFakeClient().Script(`{"fields":[],"facts":[],"people":[],"legal_entities":[]}`)

	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{SeedURL: seedURL, Brain: fakeModelPath(t, fake).SiteExtract},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}
	if report.ModelLaneError != "" {
		t.Fatalf("an empty gate result is normal, not a model-lane error: %s", report.ModelLaneError)
	}
	if report.Proposal != nil {
		t.Fatalf("nothing survived, so no proposal payload: %+v", report.Proposal)
	}
	if report.Crawl.Pages[0].Text != "" {
		t.Fatal("page text leaked into the report without IncludePageText")
	}
}
