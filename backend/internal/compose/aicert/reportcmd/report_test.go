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

func scenarioFor(task, site string) aicert.Scenario {
	return aicert.Scenario{
		Name: site + "_01", Task: task, Site: site,
		Source: "hand_authored", SanitizedBy: "tests",
		Fixture: aicert.JSONValue(`{"page":"a page"}`),
		Expect: aicert.Expectations{
			Outcome: aitasks.OutcomeAccepted,
			Bands:   aicert.Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40},
		},
	}
}

// The state a reader sees before the first paid run: every shipped site owes a
// record and none exists. It must read as "nothing has been measured", never as
// a table of zeroes — a zero count is a measurement, and there is none.
func TestReadinessReportCallsEverySiteAbsentWhenNothingIsCertified(t *testing.T) {
	sites := []aitasks.Site{
		{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot},
		{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindAgentLoop},
	}

	out := renderReadiness(sites, nil, nil)

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
	fx := scenarioFor("rate_extract", "fx")
	rank := scenarioFor("brief_ranking", "rank")
	corpus := []aicert.Scenario{fx, rank}
	records := []aicert.Record{
		{
			Task: "rate_extract", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: aicert.PromptVersion([]aicert.Scenario{fx}),
			Verdict:       aicert.VerdictCertified, Runs: 3, Accepted: 3,
		},
		{
			Task: "brief_ranking", Provider: "anthropic", ServedModel: "claude-sonnet-4-6", EnvClass: "byok",
			PromptVersion: "p0000000000000000000000000000000",
			Verdict:       aicert.VerdictCertified, Runs: 3, Accepted: 3,
		},
	}

	out := renderReadiness(sites, corpus, records)

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
	fx := scenarioFor("rate_extract", "fx")
	records := []aicert.Record{{
		Task: "rate_extract", Provider: "ollama", ServedModel: "llama3.1:8b", EnvClass: "local",
		PromptVersion:  aicert.PromptVersion([]aicert.Scenario{fx}),
		Verdict:        aicert.VerdictNotSupported,
		Runs:           9,
		Reliability:    0.22,
		Accepted:       2,
		WrongAnswer:    3,
		Invalid:        4,
		Abstained:      0,
		CertifiedScope: aitasks.ScopeFullInvocation,
	}}

	out := renderReadiness(sites, []aicert.Scenario{fx}, records)

	row := rowFor(t, out, "rate_extract/fx")
	for _, want := range []string{
		aicert.VerdictNotSupported, "0.22", "2", "3", "4", "0",
		"ollama", "llama3.1:8b", "local",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the row does not carry %q: %q", want, row)
		}
	}
	for _, want := range []string{"ACCEPTED", "WRONG_ANSWER", "INVALID", "ABSTAINED", "PROVIDER", "MODEL", "ENV"} {
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

	out := renderReadiness(sites, nil, nil)

	if got := rowFor(t, out, "rate_extract/fx"); !strings.Contains(got, aitasks.ScopeFullInvocation) {
		t.Errorf("a one-shot site does not report full_invocation scope: %q", got)
	}
	if got := rowFor(t, out, "agent_loop/loop"); !strings.Contains(got, aitasks.ScopeSingleTurn) {
		t.Errorf("the agent-loop site does not report that only one turn is certified: %q", got)
	}
}

// A row one cell short of its header prints every later value under the wrong
// column name — a misreading that looks exactly like a correct table.
func TestEveryRowFillsEveryColumn(t *testing.T) {
	site := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	rows := map[string]readinessRow{
		"absent":    {site: site},
		"certified": {site: site, certified: true, record: aicert.Record{Task: "rate_extract", Verdict: aicert.VerdictCertified}},
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

	out := renderReadiness(sites, nil, records)

	if !strings.Contains(out, "retired_task") {
		t.Errorf("a record for a task this build no longer registers vanished from the report:\n%s", out)
	}
}
