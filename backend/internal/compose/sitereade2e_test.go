// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build e2e_llm

package compose

// The deep read's pinned QUALITY FLOOR: the whole crawl→extract→merge
// pipeline against the real gradion.com with a real model, asserting the
// result a mid-tier model (baseline: Anthropic claude-sonnet-4-6,
// 2026-07-18) is known to reach. Every assertion is a FLOOR, never an
// exact match — a different or newer model passes by doing the SAME OR
// BETTER, and fails honestly when it extracts less. This is the lane an
// operator runs to judge a candidate model binding
// (`make e2e-siteread`); it costs real tokens and real network, so it
// lives behind the e2e_llm build tag and never runs in the unit or
// integration lanes.
//
// gradion.com is the fixture because its shape exercises the hard
// cases: a multilingual site (locale dedupe), a group imprint listing
// five legal entities (multi-entity abstention), and a deep consulting
// catalog (offering extraction + page-budget priorities). If the site
// is restructured someday, re-derive the floors from a fresh baseline
// run — they encode the SITE as much as the model.
//
// The split that keeps this lane and the aicert corpus from duplicating
// each other's job: crawl/pipeline assertions (coverage, dedupe, call
// count, wall-clock) live HERE, against a real crawl of a real site,
// because they are properties of the fetch+merge pipeline, not of the
// model's answer. Extraction-QUALITY assertions on a fixed prompt/input
// pair — field grounding, the no-guess floor, the legal multi-entity
// abstention — belong in aicert's corpus/site_extract/ scenarios instead,
// which pin the input once and are model-swappable without needing a
// live site.

import (
	"context"
	"os"
	"strings"
	"testing"
)

const e2eSeedURL = "https://gradion.com"

// e2eBrain builds the model under judgment from the environment — the
// test names no vendor (§1.4): MARGINCE_E2E_MODEL carries provider:model
// (e.g. anthropic:claude-sonnet-4-6), or MARGINCE_AI_ROUTING a full
// routing file. Missing both fails loudly: a quality gate that silently
// skips looks exactly like one that passed.
func e2eBrain(t *testing.T) (profile, facts completer, banner string) {
	t.Helper()
	modelSpec := os.Getenv("MARGINCE_E2E_MODEL")
	routing := os.Getenv("MARGINCE_AI_ROUTING")
	profile, facts, banner, err := SiteReadDebugBrain(routing, modelSpec, false)
	if err != nil {
		t.Fatalf("e2e-siteread needs a model: set MARGINCE_E2E_MODEL=provider:model (plus the provider's BYOK env key) or MARGINCE_AI_ROUTING: %v", err)
	}
	return profile, facts, banner
}

func TestSiteReadE2EGradionQualityFloor(t *testing.T) {
	profile, facts, banner := e2eBrain(t)
	t.Logf("model under judgment: %s", banner)

	report, err := RunSiteReadDebug(context.Background(), SiteReadDebugOptions{
		SeedURL:   e2eSeedURL,
		Brain:     profile,
		FactBrain: facts,
	})
	if err != nil {
		t.Fatalf("the read failed outright: %v", err)
	}
	if report.ModelLaneError != "" {
		t.Fatalf("the model lane died midway: %s", report.ModelLaneError)
	}
	for _, call := range report.ModelCalls {
		if call.Error != "" {
			t.Fatalf("model call errored (%s on %s): %s", call.Lane, call.PageURL, call.Error)
		}
	}

	assertCrawlCoverageFloor(t, report)
	assertFieldFloor(t, report)
	assertLegalAbstention(t, report)
	assertFactFloor(t, report)

	// The founder's speed targets (2026-07-18): frontier-wave crawl,
	// page-parallel compact extraction — ≤15s end-to-end typical; the
	// ceilings carry margin so model-latency variance cannot flake the
	// quality gate.
	// Crawl and extraction overlap: ExtractionDurationMs IS the total.
	if report.Crawl.DurationMs >= 20000 {
		t.Errorf("crawl took %d ms, ceiling 20000 (target 5000; gradion.com throttles bursts)", report.Crawl.DurationMs)
	}
	if report.ExtractionDurationMs >= 30000 {
		t.Errorf("overlapped read took %d ms, ceiling 30000 (target 15000)", report.ExtractionDurationMs)
	}
	profileCalls := 0
	for _, call := range report.ModelCalls {
		if call.Lane == laneProfile {
			profileCalls++
		}
	}
	if profileCalls != 1 {
		t.Errorf("%d profile-lane calls, want exactly one", profileCalls)
	}
	if got := len(report.ModelCalls); got > len(report.Crawl.Pages)+1 {
		t.Errorf("%d model calls for %d pages — the fan-out makes at most pages+1", got, len(report.Crawl.Pages))
	}
	// The warning-only paraphrase check must stay a WARNING rate, not a
	// firehose: more low-overlap warnings than profile fields means the
	// model cites junk passages.
	warnings := 0
	for _, d := range report.Extraction.Dropped {
		if d.Reason == dropParaphraseLowOverlap {
			warnings++
		}
	}
	if warnings > len(report.Extraction.Fields) {
		t.Errorf("%d paraphrase_low_overlap warnings for %d fields — citations are drifting", warnings, len(report.Extraction.Fields))
	}
}

// assertCrawlCoverageFloor pins the crawl half: the locale dedupe and
// kind priorities must leave the budget on distinct, fact-bearing pages.
// Baseline read 40 pages: 15 products, 4 services, 4 team, 9 about.
func assertCrawlCoverageFloor(t *testing.T, r SiteReadDebugReport) {
	t.Helper()
	if got := len(r.Crawl.Pages); got < 30 {
		t.Errorf("crawl coverage floor: read %d pages, baseline reads 40 (floor 30)", got)
	}
	kinds := map[string]int{}
	localeDupes := 0
	seenCanonical := map[string]bool{}
	for _, page := range r.Crawl.Pages {
		kinds[page.Kind]++
		canon := localeCanonical(page.URL)
		if seenCanonical[canon] && page.Kind != "impressum" {
			localeDupes++
		}
		seenCanonical[canon] = true
	}
	if kinds["products"]+kinds["services"] < 10 {
		t.Errorf("offering-page floor: %d products + %d services pages, floor 10 combined — the catalog is being cut", kinds["products"], kinds["services"])
	}
	if localeDupes > 0 {
		t.Errorf("%d non-legal locale duplicates were fetched — the budget is leaking into translations", localeDupes)
	}
}

// assertFieldFloor pins the company-field half. The baseline grounds all
// eight non-legal cold-start fields; the floor allows two misses (model
// nondeterminism between runs) but never a thin read.
func assertFieldFloor(t *testing.T, r SiteReadDebugReport) {
	t.Helper()
	got := map[string]string{}
	for _, f := range r.Extraction.Fields {
		got[f.Field] = f.Value
	}
	if name, ok := got["display_name"]; !ok || !strings.Contains(strings.ToLower(name), "gradion") {
		t.Errorf("display_name floor: want a value naming Gradion, got %q", name)
	}
	baseline := []string{"display_name", "icp", "buying_center", "history", "industry", "value_proposition", "usp", "buying_intents"}
	present := 0
	for _, field := range baseline {
		if got[field] != "" {
			present++
		}
	}
	if present < len(baseline)-2 {
		t.Errorf("field floor: %d of the %d baseline fields grounded (%v), floor is all but two (run-to-run model variance)", present, len(baseline), got)
	}
}

// assertLegalAbstention pins the wrong-company behaviour: gradion.com's
// imprint lists five legal entities, so the ONLY correct answer is no
// legal identity at all plus the multi-entity warning. A model that
// "finds" a legal_name here is confidently wrong, whatever else it does
// better.
func assertLegalAbstention(t *testing.T, r SiteReadDebugReport) {
	t.Helper()
	for _, f := range r.Extraction.Fields {
		switch f.Field {
		case "legal_name", "registered_address", "register_vat":
			t.Errorf("multi-entity abstention violated: %s = %q proposed from %s", f.Field, f.Value, f.SourceURL)
		}
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "disagreeing legal pages") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the multi-entity warning is missing: %v", r.Warnings)
	}
}

// assertFactFloor pins the category-fact half. Baseline: 82 facts, the
// offering lane rich from the consulting catalog.
func assertFactFloor(t *testing.T, r SiteReadDebugReport) {
	t.Helper()
	if got := len(r.Extraction.Facts); got < 40 {
		t.Errorf("fact floor: %d facts, baseline extracts 82 (floor 40)", got)
	}
	perCategory := map[string]int{}
	for _, f := range r.Extraction.Facts {
		perCategory[f.Category]++
	}
	if perCategory["offering"] < 10 {
		t.Errorf("offering floor: %d offering facts, floor 10 — the consulting catalog is not being extracted", perCategory["offering"])
	}
	if perCategory["signal"] < 5 {
		t.Errorf("signal floor: %d signal facts (partners, customers, certifications), floor 5", perCategory["signal"])
	}

	// The taxonomy's v2 rows (founder targets 2026-07-18): gradion.com
	// publishes five offices and names its platforms; the read must land
	// them.
	perField := map[string]int{}
	for _, f := range r.Extraction.Facts {
		perField[f.Field]++
	}
	if perField["location"] < 4 {
		t.Errorf("location floor: %d company/location facts, floor 4 (SG/TH/VN/DE/EG are published)", perField["location"])
	}
	if perField["technology"] < 5 {
		t.Errorf("technology floor: %d signal/technology facts, floor 5 (Shopware/Spryker/AWS-class names are published)", perField["technology"])
	}
}
