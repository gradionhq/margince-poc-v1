// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The debug facade's contract: it runs the worker's exact
// crawl→parallel-lanes→gate spine and reports every intermediate —
// pages, call telemetry, drops, the legal-entity census, timings, and
// the byte-identical proposal payload — without a database or staging.

import (
	"context"
	"testing"
)

func TestSiteReadDebugReportsPagesLanesAndProposal(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Acme home.") + " Onboard your team in minutes, not weeks with our rollout playbooks."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart."},
	}}
	brain := laneFake{
		// Excerpt ids are global over the rank-sorted pages: the imprint
		// leads, so s0 is its passage and s1 the home page's.
		profileReply: `{"fields":[
			{"f":"value_proposition","v":"Fast onboarding with rollout playbooks","e":"s1","c":0.9},
			{"f":"legal_name","v":"Acme Robotics GmbH","e":"s0","c":0.9}]}`,
		pageReplies: map[string]string{
			seedURL + "/impressum": `{"facts":[{"f":"location","v":"Stuttgart","e":"s0"}],"entities":[{"n":"Acme Robotics GmbH","e":"s0"}]}`,
		},
	}

	var phases []string
	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{
			SeedURL: seedURL, Brain: brain, FactBrain: brain, IncludePageText: true,
			Progress: func(phase string, done, total int) { phases = append(phases, phase) },
		},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}
	if report.ModelLaneError != "" {
		t.Fatalf("clean lanes reported an error: %s", report.ModelLaneError)
	}

	if got := len(report.Crawl.Pages); got != 2 {
		t.Fatalf("reported %d pages, want 2: %+v", got, report.Crawl.Pages)
	}
	if report.Crawl.Pages[0].Text == "" {
		t.Fatal("IncludePageText was set but the page text is missing")
	}
	if len(phases) < 2 {
		t.Fatalf("progress must fire for crawl and pages: %v", phases)
	}

	byName := map[string]DebugField{}
	for _, f := range report.Extraction.Fields {
		byName[f.Field] = f
	}
	if byName["legal_name"].Value != "Acme Robotics GmbH" || byName["legal_name"].SourceURL != seedURL+"/impressum" {
		t.Fatalf("the single-entity legal trio must stand, resolver-attributed: %+v", report.Extraction.Fields)
	}
	if len(report.Extraction.Facts) != 1 || report.Extraction.Facts[0].Field != "location" {
		t.Fatalf("the location fact is missing: %+v", report.Extraction.Facts)
	}
	if len(report.Extraction.LegalEntities) != 1 {
		t.Fatalf("the census must be reported: %+v", report.Extraction.LegalEntities)
	}

	// Two fact-bearing pages (home, impressum) + the profile call.
	if got := len(report.ModelCalls); got != 3 {
		t.Fatalf("recorded %d model calls, want 3 (2 page + 1 profile): %+v", got, report.ModelCalls)
	}
	lanes := map[string]int{}
	for _, call := range report.ModelCalls {
		lanes[call.Lane]++
	}
	if lanes[laneProfile] != 1 || lanes[lanePageFacts] != 2 {
		t.Fatalf("lanes = %v, want 1 profile + 2 page_facts", lanes)
	}
	if report.ExtractionDurationMs < 0 {
		t.Fatal("extraction duration missing")
	}

	if report.Proposal == nil {
		t.Fatal("findings survived but the report carries no proposal payload")
	}
	if len(report.Proposal.Fields) != len(report.Extraction.Fields) || len(report.Proposal.Facts) != len(report.Extraction.Facts) {
		t.Fatalf("the proposal payload disagrees with the reported extraction: %+v vs %+v",
			report.Proposal, report.Extraction)
	}
}

func TestSiteReadDebugEmptyLanesReportCleanWithNoLaneError(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL: {text: readable("Acme home.")},
	}}
	brain := laneFake{profileReply: `{"fields":[]}`}

	report, err := siteReadDebugRun(context.Background(),
		SiteReadDebugOptions{SeedURL: seedURL, Brain: brain, FactBrain: brain},
		testSiteCrawler(site), nil)
	if err != nil {
		t.Fatalf("siteReadDebugRun: %v", err)
	}
	if report.ModelLaneError != "" {
		t.Fatalf("an empty gate result is normal, not a lane error: %s", report.ModelLaneError)
	}
	if report.Proposal != nil {
		t.Fatalf("nothing survived, so no proposal payload: %+v", report.Proposal)
	}
	if report.Crawl.Pages[0].Text != "" {
		t.Fatal("page text leaked into the report without IncludePageText")
	}
}
