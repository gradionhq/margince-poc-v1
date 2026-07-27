// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// rowFor returns the one rendered line naming site, so an assertion about a
// site's state cannot accidentally be satisfied by another site's row.
func rowFor(t *testing.T, out, site string) string {
	t.Helper()
	var found string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), site+" ") {
			continue
		}
		if found != "" {
			t.Fatalf("site %s has more than one row:\n%s", site, out)
		}
		found = line
	}
	if found == "" {
		t.Fatalf("no row for site %s in:\n%s", site, out)
	}
	return found
}

// scenarioRecordFor is one all-passing scenario row on a site, which is what a
// record needs to carry for the report to attribute it to that site at all.
// Three runs, three passes, three accepted replies: the verdict is not a
// parameter because those numbers leave it nothing else to be.
func scenarioRecordFor(site string) aicert.ScenarioRecord {
	return aicert.ScenarioRecord{
		Scenario: site + "_01", Site: site, Verdict: aicert.VerdictCertified,
		Runs: 3, Passed: 3, ReportedAccepted: 3,
	}
}

// The stamps a report is handed are opaque to it: main computes them from the
// corpus and the census (aicert.PromptVersion), and the report only ever asks
// whether a record carries the one its task computes. So these tests name them
// rather than compute them — the digest's own tests own what moves it.
const currentStamp = "p00000000000000000000000000000000"

// staleStamp is any other value: a record carrying it was scored against
// something this build no longer sends.
const staleStamp = "p11111111111111111111111111111111"

// The state a reader sees before the first paid run: every shipped site owes a
// record and none exists. It must read as "nothing has been measured", never as
// a table of zeroes — a zero count is a measurement, and there is none.
func TestReadinessReportCallsEverySiteAbsentWhenNothingIsCertified(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindAgentLoop},
	}

	out := renderReadiness(shippedCensus{sites: sites}, nil, nil)

	for _, site := range []string{"rate_extract/fx", "agent_loop/loop"} {
		row := rowFor(t, out, site)
		if !strings.Contains(row, "absent") {
			t.Errorf("row for %s does not say the record is absent: %q", site, row)
		}
		if strings.Contains(row, "certified") || strings.Contains(row, " 0 ") {
			t.Errorf("row for %s reports a measurement nobody made: %q", site, row)
		}
	}
	if !strings.Contains(out, "make e2e-ai") {
		t.Errorf("the report does not say how to produce the missing records:\n%s", out)
	}
}

// Staleness and absence are different claims: a stale record asserts a verdict
// about prompts this build no longer sends, an absent one asserts nothing. A
// reader who cannot tell them apart cannot tell a lie from a gap.
func TestReadinessReportRendersStaleAndAbsentDistinctly(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskBriefRanking, Variant: "rank", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindAgentLoop},
	}
	stamps := map[string]string{"rate_extract": currentStamp, "brief_ranking": currentStamp}
	records := []aicert.Record{
		{
			Task: "rate_extract", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: currentStamp,
			Verdict:       aicert.VerdictCertified, Runs: 3, Passed: 3,
			Scenarios: []aicert.ScenarioRecord{scenarioRecordFor("fx")},
		},
		{
			Task: "brief_ranking", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: staleStamp,
			Verdict:       aicert.VerdictCertified, Runs: 3, Passed: 3,
			Scenarios: []aicert.ScenarioRecord{scenarioRecordFor("rank")},
		},
	}

	out := renderReadiness(shippedCensus{sites: sites}, stamps, records)

	fresh := rowFor(t, out, "rate_extract/fx")
	if !strings.Contains(fresh, "current") {
		t.Errorf("the row whose stamp matches this corpus is not marked current: %q", fresh)
	}
	if strings.Contains(fresh, "stale") || strings.Contains(fresh, "absent") {
		t.Errorf("a current record's row also claims another state: %q", fresh)
	}
	stale := rowFor(t, out, "brief_ranking/rank")
	if !strings.Contains(stale, "stale") {
		t.Errorf("a record stamped against a corpus that has since changed is not marked stale: %q", stale)
	}
	if strings.Contains(stale, "absent") {
		t.Errorf("a stale record's row reads as absent, collapsing a lie into a gap: %q", stale)
	}
	missing := rowFor(t, out, "agent_loop/loop")
	if !strings.Contains(missing, "absent") || strings.Contains(missing, "stale") {
		t.Errorf("the site with no record at all is not rendered as absent: %q", missing)
	}
	if !strings.Contains(out, "stale") || !strings.Contains(strings.ToLower(out), "no longer") {
		t.Errorf("the report never says what stale means:\n%s", out)
	}
}

// The verdict alone cannot be acted on: a refused reply and a well-formed wrong
// answer want opposite fixes. The counts the record now carries are what turn a
// band into a diagnosis, so the report must show all four.
func TestReadinessReportCarriesTheBandTheCountsAndTheBinding(t *testing.T) {
	sites := []aitasks.Site{{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}}
	records := []aicert.Record{{
		Task: "rate_extract", Provider: "ollama", ServedModel: "llama3.1:8b", EnvClass: "local",
		PromptVersion:  currentStamp,
		Verdict:        aicert.VerdictNotSupported,
		Runs:           9,
		Passed:         2,
		Reliability:    0.22,
		CertifiedScope: aitasks.ScopeFullInvocation,
		Scenarios: []aicert.ScenarioRecord{{
			Scenario: "fx_01", Site: "fx", Verdict: aicert.VerdictNotSupported,
			Runs: 9, Passed: 2,
			ReportedAccepted: 2, ReportedWrongAnswer: 3, ReportedInvalid: 4,
		}},
	}}

	out := renderReadiness(shippedCensus{sites: sites}, map[string]string{"rate_extract": currentStamp}, records)

	row := rowFor(t, out, "rate_extract/fx")
	for _, want := range []string{
		aicert.VerdictNotSupported, "0.22", "2", "3", "4", "0",
		"ollama", "llama3.1:8b", "local",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the row does not carry %q: %q", want, row)
		}
	}
	for _, want := range []string{"RUNS", "PASSED", "ACCEPTED", "WRONG_ANSWER", "INVALID", "ABSTAINED", "PROVIDER", "MODEL", "ENV"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report has no %s column:\n%s", want, out)
		}
	}
	// The binding is part of the claim: a record certifies one deployment and
	// green-lights no other, and a release decision reads that off this report.
	if !strings.Contains(out, "provider") || !strings.Contains(out, "env") {
		t.Errorf("the report never names what a row's binding means:\n%s", out)
	}
}

// The agent loop is the site whose certification covers one turn of a loop, and
// the report is where that stops being a comment in the code. It has to be
// legible with no record at all, because that is the state today.
func TestReadinessReportShowsTheScopeEachSiteCanClaim(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindAgentLoop},
	}

	out := renderReadiness(shippedCensus{sites: sites}, nil, nil)

	if got := rowFor(t, out, "rate_extract/fx"); !strings.Contains(got, aitasks.ScopeFullInvocation) {
		t.Errorf("a one-shot site does not report full_invocation scope: %q", got)
	}
	if got := rowFor(t, out, "agent_loop/loop"); !strings.Contains(got, aitasks.ScopeSingleTurn) {
		t.Errorf("the agent-loop site does not report that only one turn is certified: %q", got)
	}
}

// A one-shot site whose case reaches one of the calls the site makes claims less
// than its kind. The report reads the CASE's answer, or the shape of the site
// would keep saying full_invocation for a run that could never be one.
func TestReadinessReportShowsTheScopeACaseNarrowedItselfTo(t *testing.T) {
	site := aitasks.Site{Task: ai.TaskCaptureClassify, Variant: "classify", Kind: ai.SiteKindOneShot}
	census := shippedCensus{
		sites:  []aitasks.Site{site},
		scopes: map[string]string{"capture_classify/classify": aitasks.ScopeSingleCall},
	}

	out := renderReadiness(census, nil, nil)

	got := rowFor(t, out, "capture_classify/classify")
	if !strings.Contains(got, aitasks.ScopeSingleCall) {
		t.Errorf("the row reports %q, want the scope the case narrowed itself to (%q)", got, aitasks.ScopeSingleCall)
	}
	if strings.Contains(got, aitasks.ScopeFullInvocation) {
		t.Errorf("the row still claims the whole invocation on the strength of the site's kind: %q", got)
	}
}

// A row one cell short of its header prints every later value under the wrong
// column name — a misreading that looks exactly like a correct table.
func TestEveryRowFillsEveryColumn(t *testing.T) {
	site := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	rows := map[string]readinessRow{
		"absent": {site: site},
		"certified": {
			site: site, certified: true,
			record: aicert.Record{Task: "rate_extract"},
			tally:  aicert.SiteTally{Verdict: aicert.VerdictCertified, Runs: 3, Passed: 3},
		},
	}
	for state, row := range rows {
		if got := len(row.cells()); got != len(reportColumns) {
			t.Errorf("a %s row renders %d cells under %d column headers", state, got, len(reportColumns))
		}
	}
}

// A record whose task no shipped site claims is still a committed artifact. It
// must be named rather than dropped: a record nobody enumerates reads as no
// record at all, which is the same failure this whole report exists to remove.
func TestReadinessReportNamesRecordsNoShippedSiteClaims(t *testing.T) {
	sites := []aitasks.Site{{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}}
	records := []aicert.Record{{
		Task: "retired_task", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
		Verdict: aicert.VerdictCertified, Runs: 3,
	}}

	out := renderReadiness(shippedCensus{sites: sites}, nil, records)

	if !strings.Contains(out, "retired_task") {
		t.Errorf("a record for a task this build no longer registers vanished from the report:\n%s", out)
	}
}

// One record covers a task, and cold_start ships four sites. Printing the
// task's pooled numbers on each site's row gives four identical rows wearing
// four labels — a reader cannot tell which site was measured how, which is the
// whole reason the row is the site.
func TestReadinessReportGivesEachSiteItsOwnNumbers(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskColdStart, Variant: "acts", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskColdStart, Variant: "field_extract", Kind: ai.SiteKindOneShot},
	}
	records := []aicert.Record{{
		Task: "cold_start", Provider: "gemini", ServedModel: "gemini-2.5-flash", EnvClass: "byok",
		PromptVersion: currentStamp,
		Verdict:       aicert.VerdictNotSupported, Runs: 6, Passed: 3,
		Scenarios: []aicert.ScenarioRecord{
			{Scenario: "acts_01", Site: "acts", Verdict: aicert.VerdictCertified, Runs: 3, Passed: 3, ReportedAccepted: 3},
			{Scenario: "extract_01", Site: "field_extract", Verdict: aicert.VerdictNotSupported, Runs: 3, Passed: 0, ReportedInvalid: 3},
		},
	}}

	out := renderReadiness(shippedCensus{sites: sites}, map[string]string{"cold_start": currentStamp}, records)

	acts := rowFor(t, out, "cold_start/acts")
	if !strings.Contains(acts, aicert.VerdictCertified) || !strings.Contains(acts, "1.00") {
		t.Errorf("the site whose scenarios all passed does not read that way: %q", acts)
	}
	extract := rowFor(t, out, "cold_start/field_extract")
	if !strings.Contains(extract, aicert.VerdictNotSupported) || !strings.Contains(extract, "0.00") {
		t.Errorf("the site whose scenarios all failed does not read that way: %q", extract)
	}
	if acts == extract {
		t.Errorf("both sites of one task render the identical row:\n%s", out)
	}
}

// A record whose task ships but whose scenarios name no site of it cannot be
// attributed to any row. It must still be named: a record nobody enumerates
// reads as no record at all.
func TestReadinessReportNamesARecordNoSiteRowCanCarry(t *testing.T) {
	sites := []aitasks.Site{{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}}
	records := []aicert.Record{{
		Task: "rate_extract", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
		PromptVersion: currentStamp, Verdict: aicert.VerdictCertified, Runs: 3, Passed: 3,
		Scenarios: []aicert.ScenarioRecord{scenarioRecordFor("a_site_that_moved")},
	}}

	out := renderReadiness(shippedCensus{sites: sites}, map[string]string{"rate_extract": currentStamp}, records)

	if row := rowFor(t, out, "rate_extract/fx"); !strings.Contains(row, "absent") {
		t.Errorf("a site this record never measured is not reported as unmeasured: %q", row)
	}
	if !strings.Contains(out, "no row above can attribute it") {
		t.Errorf("the unattributable record vanished from the report:\n%s", out)
	}
}

// Every run in this lane is served the site's own prompt and nothing else,
// because assembling the company context reads a database no certification run
// has. For most tasks that is a difference of nothing — their contract prepends
// no context either. For the ones whose contract does, the certified prompt is
// short exactly what production always supplies, and no column of the table can
// say so: the report has to.
func TestReadinessReportNamesTheTasksCertifiedWithoutTheirCompanyContext(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskOfferDraft, Variant: "draft", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot},
	}
	records := []aicert.Record{
		{
			Task: "offer_draft", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: currentStamp, Verdict: aicert.VerdictCertified, Runs: 3, Passed: 3,
			ContextScopes: []string{"offer", "positioning", "proof"},
			Scenarios:     []aicert.ScenarioRecord{scenarioRecordFor("draft")},
		},
		{
			Task: "rate_extract", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: currentStamp, Verdict: aicert.VerdictCertified, Runs: 3, Passed: 3,
			Scenarios: []aicert.ScenarioRecord{scenarioRecordFor("fx")},
		},
	}
	stamps := map[string]string{"offer_draft": currentStamp, "rate_extract": currentStamp}

	out := renderReadiness(shippedCensus{sites: sites}, stamps, records)

	if !strings.Contains(out, "offer_draft (offer, positioning, proof)") {
		t.Errorf("the report never says which reference data offer_draft was certified without:\n%s", out)
	}
	// The task that prepends nothing lost nothing, and listing it would bury the
	// one that did.
	if strings.Contains(out, "rate_extract (") {
		t.Errorf("a task whose contract prepends no company context is named as having gone without one:\n%s", out)
	}
}
