// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The debug facade's contract: it runs the worker's exact
// crawl→extract→merge spine and reports every intermediate — pages,
// per-call telemetry, merge decisions, and the byte-identical proposal
// payload — without a database or staging.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestSiteReadDebugReportsPagesMergesAndModelCalls(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Acme home.") + " Onboard your team in minutes, not weeks. Built for RevOps leaders at scaling SaaS companies."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart."},
	}}
	// Two calls per crawled page in crawl order: home fields + home
	// signal, then Impressum fields + Impressum company. The home reply
	// grounds a guessed legal name the Impressum must override.
	brain := ai.NewFakeClient().Script(
		`{"fields":[
			{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
			{"field":"legal_name","value":"Acme (guessed)","evidence_snippet":"Built for RevOps leaders","confidence":0.95}]}`,
		`{"fields":[
			{"field":"named_customer","value":"Scaling SaaS companies — stated audience","evidence_snippet":"scaling SaaS companies","confidence":0.6}]}`,
		`{"fields":[
			{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","confidence":0.7}]}`,
		`{"fields":[]}`,
		// The site-level synthesis pass finds nothing to correct.
		`{"fields":[]}`,
	)

	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{SeedURL: seedURL, Brain: brain, IncludePageText: true},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}

	if got := len(report.Crawl.Pages); got != 2 {
		t.Fatalf("reported %d pages, want 2 (home + impressum): %+v", got, report.Crawl.Pages)
	}
	if !report.Crawl.Pages[0].Extracted || !report.Crawl.Pages[1].Extracted {
		t.Fatalf("every crawled page got its model pass, but Extracted says otherwise: %+v", report.Crawl.Pages)
	}
	if report.Crawl.Pages[0].Text == "" {
		t.Fatal("IncludePageText was set but the page text is missing from the report")
	}

	fieldsByName := map[string]DebugField{}
	for _, f := range report.Extraction.Fields {
		fieldsByName[f.Field] = f
	}
	legal, ok := fieldsByName["legal_name"]
	if !ok || legal.Value != "Acme Robotics GmbH" {
		t.Fatalf("legal_name should be the Impressum's answer, got %+v", fieldsByName)
	}
	if legal.SourceURL != seedURL+"/impressum" {
		t.Fatalf("legal_name source = %s, want the Impressum page", legal.SourceURL)
	}

	var legalDecision *DebugMergeDecision
	for i, d := range report.Extraction.MergeDecisions {
		if d.Field == "legal_name" {
			legalDecision = &report.Extraction.MergeDecisions[i]
		}
	}
	if legalDecision == nil {
		t.Fatalf("the legal_name conflict left no merge decision: %+v", report.Extraction.MergeDecisions)
	}
	if legalDecision.WinnerSource != seedURL+"/impressum" || len(legalDecision.Losers) != 1 {
		t.Fatalf("merge decision should name the Impressum winner and the home-page loser: %+v", legalDecision)
	}

	if got := len(report.ModelCalls); got != 5 {
		t.Fatalf("recorded %d model calls, want 5 (2 pages × 2 lanes + synthesis): %+v", got, report.ModelCalls)
	}
	wantLanes := []string{"fields", "category:signal", "fields", "category:company", "synthesis"}
	for i, call := range report.ModelCalls {
		if call.Lane != wantLanes[i] {
			t.Fatalf("call %d lane = %s, want %s", i, call.Lane, wantLanes[i])
		}
	}
	if report.ModelCalls[0].PageURL != seedURL || report.ModelCalls[2].PageURL != seedURL+"/impressum" {
		t.Fatalf("calls are not attributed to their pages: %+v", report.ModelCalls)
	}

	if report.Proposal == nil {
		t.Fatal("fields and facts survived but the report carries no proposal payload")
	}
	if len(report.Proposal.Fields) != len(report.Extraction.Fields) || len(report.Proposal.Facts) != len(report.Extraction.Facts) {
		t.Fatalf("the proposal payload disagrees with the reported extraction: %+v vs %+v",
			report.Proposal, report.Extraction)
	}
}

func TestSiteReadDebugGateEmptyPagesReportCleanWithNoLaneError(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Acme home.") + " Onboard your team in minutes, not weeks."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH."},
	}}
	// The home page's two passes are scripted; the Impressum's calls run
	// off the script into the fake's non-JSON fallback, which gates to
	// zero fields without erroring — the honest "nothing evidenced" path.
	brain := ai.NewFakeClient().Script(
		`{"fields":[{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9}]}`,
		`{"fields":[]}`,
	)

	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{SeedURL: seedURL, Brain: brain},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}
	if report.ModelLaneError != "" {
		t.Fatalf("gate-empty pages are normal, not a model-lane error: %s", report.ModelLaneError)
	}
	if len(report.Extraction.Fields) != 1 {
		t.Fatalf("the home page's evidenced field is missing: %+v", report.Extraction.Fields)
	}
	if report.Crawl.Pages[0].Text != "" {
		t.Fatal("page text leaked into the report without IncludePageText")
	}
}
