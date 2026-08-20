// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"strings"
	"testing"
)

// synthDomains builds a domain list of the requested size. Names are varied
// enough that the hash spreads them; a list of "a1.de", "a2.de"… would test
// the counter rather than the hash.
func synthDomains(n int) []string {
	words := []string{"nord", "hansa", "vogel", "stein", "linde", "faber", "kranz", "beck", "ruhr", "elbe"}
	tlds := []string{"de", "com", "io", "eu"}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s%d.%s", words[i%len(words)], i, tlds[i%len(tlds)]))
	}
	return out
}

func TestPlanCoversTheMatrix(t *testing.T) {
	// The matrix must be satisfiable at the sizes this dataset actually
	// reaches. 60 is roughly where the crawl is now; 200 is the target.
	for _, size := range []int{60, 120, 200, 300} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			profiles := planProfiles(synthDomains(size), demoConfig{})
			short := coverageShortfall(planningMatrix(), countCoverage(profiles))
			if len(short) > 0 {
				t.Fatalf("coverage not reached at %d companies:\n  %s", size, strings.Join(short, "\n  "))
			}
		})
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	domains := synthDomains(200)
	first := planProfiles(domains, demoConfig{})

	// Same input, different order: the planner sorts, so the result must be
	// identical. Map iteration order alone would break this.
	shuffled := make([]string, len(domains))
	for i, d := range domains {
		shuffled[len(domains)-1-i] = d
	}
	second := planProfiles(shuffled, demoConfig{})

	if len(first) != len(second) {
		t.Fatalf("sizes differ: %d vs %d", len(first), len(second))
	}
	for domain, a := range first {
		b := second[domain]
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%s differs between runs:\n  %+v\n  %+v", domain, a, b)
		}
	}
}

// TestPlanIsStableUnderInsert is the property that matters most in practice:
// crawling ten more companies next month must not reshuffle the book. A
// re-seed that moved customers around would invalidate every screenshot, every
// bug report and every bookmark.
func TestPlanIsStableUnderInsert(t *testing.T) {
	before := synthDomains(200)
	after := synthDomains(230)

	planBefore := planProfiles(before, demoConfig{})
	planAfter := planProfiles(after, demoConfig{})

	changed := 0
	for domain, was := range planBefore {
		now := planAfter[domain]
		if fmt.Sprint(was) != fmt.Sprint(now) {
			changed++
			if changed <= 5 {
				t.Logf("%s changed:\n  was %+v\n  now %+v", domain, was, now)
			}
		}
	}
	// Some churn is unavoidable and correct: a newcomer that outranks an
	// existing promotee for a scarce cell should take it, which demotes one
	// company. The bound is one displacement per cell.
	limit := len(coverageMatrix)
	if changed > limit {
		t.Errorf("%d of %d companies changed profile after adding 30; at most %d (one per coverage cell) is expected",
			changed, len(planBefore), limit)
	}
}

// TestPinnedCompaniesAreLeftAlone proves demo.json still wins. The five story
// customers carry hand-authored renewal chains and payment histories; a
// generator overwriting them would destroy the demo's narrative.
func TestPinnedCompaniesAreLeftAlone(t *testing.T) {
	domains := append(synthDomains(150), "akeneo.com", "trbo.com")
	cfg := demoConfig{
		Deals:     []demoDeal{{Company: "akeneo.com", Name: "Akeneo Expansion"}},
		Contracts: []demoContract{{Company: "trbo.com", Title: "trbo Rahmenvertrag"}},
		Lifecycle: map[string][]string{"customer": {"akeneo.com"}},
	}
	profiles := planProfiles(domains, cfg)

	for _, domain := range []string{"akeneo.com", "trbo.com"} {
		p := profiles[domain]
		if !p.Pinned {
			t.Errorf("%s is named by demo.json but was not pinned", domain)
		}
		if p.DealStage != "" || len(p.Contracts) > 0 || p.LeadState != "" {
			t.Errorf("%s is pinned but the planner gave it records: %+v", domain, p)
		}
	}
	if got := profiles["akeneo.com"].Lifecycle; got != "customer" {
		t.Errorf("akeneo.com lifecycle = %q, want the demo.json value \"customer\"", got)
	}
}

// TestProfilesAreCoherent catches a promotion that satisfies the matrix by
// producing a record nobody would believe — an untouched target holding a
// signed contract, or a customer sitting in the lead funnel.
func TestProfilesAreCoherent(t *testing.T) {
	for _, p := range planProfiles(synthDomains(250), demoConfig{}) {
		if p.Pinned {
			continue
		}
		switch p.Lifecycle {
		case "target", "prospect":
			for _, status := range p.Contracts {
				if status == "active" || status == "superseded" {
					t.Errorf("%s is a %s holding a %s contract", p.Domain, p.Lifecycle, status)
				}
			}
			if p.Project != "" {
				t.Errorf("%s is a %s with a project", p.Domain, p.Lifecycle)
			}
		case "customer", "former_customer":
			if p.LeadState != "" {
				t.Errorf("%s is a %s and also a %s lead", p.Domain, p.Lifecycle, p.LeadState)
			}
			if p.DealStage != "won" {
				t.Errorf("%s is a %s whose deal is %q, not won", p.Domain, p.Lifecycle, p.DealStage)
			}
		}
		if p.DealStage == "lost" && p.LostReason == "" {
			t.Errorf("%s has a lost deal with no reason", p.Domain)
		}
		if p.DealStage != "lost" && p.LostReason != "" {
			t.Errorf("%s carries a lost reason on a %q deal", p.Domain, p.DealStage)
		}
	}
}

// TestEveryLostReasonAppears — the reason filter needs something to filter,
// and a reason nobody uses is a dead branch in the UI.
func TestEveryLostReasonAppears(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range planProfiles(synthDomains(200), demoConfig{}) {
		if p.LostReason != "" {
			seen[p.LostReason] = true
		}
	}
	for _, reason := range lostReasons {
		if !seen[reason] {
			t.Errorf("no company carries lost reason %q", reason)
		}
	}
}

// TestSmallDatasetDoesNotPanic — the crawl grows over time, and the planner
// must degrade honestly rather than crash when the matrix cannot be met.
func TestSmallDatasetDoesNotPanic(t *testing.T) {
	for _, size := range []int{0, 1, 5, 20} {
		profiles := planProfiles(synthDomains(size), demoConfig{})
		if len(profiles) != size {
			t.Errorf("size %d: got %d profiles", size, len(profiles))
		}
		// Shortfalls are expected here; the point is that it returns.
		coverageShortfall(planningMatrix(), countCoverage(profiles))
	}
}

func TestMinCompaniesForCoverageIsHonest(t *testing.T) {
	need := minCompaniesForCoverage()
	if need <= 0 {
		t.Fatalf("minCompaniesForCoverage() = %d", need)
	}
	// The matrix must be satisfiable at the size it claims to need.
	profiles := planProfiles(synthDomains(need), demoConfig{})
	if short := coverageShortfall(planningMatrix(), countCoverage(profiles)); len(short) > 0 {
		t.Errorf("minCompaniesForCoverage() says %d is enough, but coverage is short:\n  %s",
			need, strings.Join(short, "\n  "))
	}
}

// TestLeadIsAssignedSplitsInHalf pins the lead assignment split. Half the
// generated leads must be left unassigned so the queue-and-claim screens have
// something to show; before this, every generated lead was owned on creation.
//
// The split is by position, not by hash, and the test says why: on the 45
// domains that actually carry a generated lead, every hash salt tried landed
// on 62/38. A hash promises "about half" only in the large-sample limit, and
// 45 is not that.
func TestLeadIsAssignedSplitsInHalf(t *testing.T) {
	for _, n := range []int{2, 10, 45, 100, 199} {
		assigned := 0
		for i := 0; i < n; i++ {
			if leadIsAssigned(i) {
				assigned++
			}
		}
		want := (n + 1) / 2 // index 0 is assigned, so odd counts round up
		if assigned != want {
			t.Errorf("with %d leads: assigned %d, want %d", n, assigned, want)
		}
		if unassigned := n - assigned; unassigned == 0 {
			t.Errorf("with %d leads: nothing left unassigned — the claim queue is empty", n)
		}
	}
}

// TestLeadIsAssignedIsStable is the convergence contract: a given rank must
// always land in the same bucket, or a second seed moves a lead from a rep's
// queue to nobody's.
//
// The buckets are pinned as literal values rather than recomputed from the
// function, so that changing the rule fails here instead of silently
// reshuffling every demo installation's lead queue on its next re-seed.
func TestLeadIsAssignedIsStable(t *testing.T) {
	want := []bool{true, false, true, false, true, false, true, false}
	for i, w := range want {
		if got := leadIsAssigned(i); got != w {
			t.Errorf("leadIsAssigned(%d) = %v, want %v", i, got, w)
		}
	}
}

// leadPlan builds a plan where every named domain carries a lead, plus a
// pinned company and a company with no lead, which leadAssignRank must skip.
func leadPlan(domains ...string) map[string]profile {
	plan := map[string]profile{
		"pinned.de": {Domain: "pinned.de", Pinned: true, LeadState: "new"},
		"nolead.de": {Domain: "nolead.de"},
	}
	for _, d := range domains {
		plan[d] = profile{Domain: d, LeadState: "new"}
	}
	return plan
}

// TestLeadAssignRankSkipsNonLeadCompanies keeps the rank dense over the
// domains that actually carry a lead. A pinned company or one with no lead
// must not consume a rank, or the halves stop being halves.
func TestLeadAssignRankSkipsNonLeadCompanies(t *testing.T) {
	rank := leadAssignRank(leadPlan("a.de", "b.de", "c.de", "d.de"))
	if len(rank) != 4 {
		t.Fatalf("ranked %d domains, want 4 (the pinned and lead-less ones must be skipped)", len(rank))
	}
	if _, ok := rank["pinned.de"]; ok {
		t.Error("a pinned company was ranked")
	}
	if _, ok := rank["nolead.de"]; ok {
		t.Error("a company with no lead was ranked")
	}
	assigned := 0
	for _, r := range rank {
		if leadIsAssigned(r) {
			assigned++
		}
	}
	if assigned != 2 {
		t.Errorf("assigned %d of 4, want exactly half", assigned)
	}
}

// TestLeadAssignRankIsIndependentOfRunHistory is the finding this test exists
// for: which half a lead falls in must be a property of the DOMAIN and of the
// plan, never of which run happened to create it.
//
// An earlier version counted positions as the SEEDING LOOP walked them, so it
// also skipped any domain whose organization was missing from the
// installation. A `-limit N` run holds a subset of the organizations, so the
// same domain got a different count on a limited run than on a full one — and
// because leads already on file are never moved, an installation's 50/50
// split ended up depending on the order its runs happened in.
//
// Ranking over the plan removes the run from the equation entirely: the plan
// does not know which organizations exist, so neither does the rank.
func TestLeadAssignRankIsIndependentOfRunHistory(t *testing.T) {
	domains := []string{"a.de", "b.de", "c.de", "d.de", "e.de", "f.de"}
	full := leadAssignRank(leadPlan(domains...))

	// The same plan ranks identically however many times it is walked. This
	// is the convergence contract: a re-seed must not move a lead.
	for i := 0; i < 3; i++ {
		again := leadAssignRank(leadPlan(domains...))
		for d, r := range full {
			if again[d] != r {
				t.Fatalf("%s ranked %d then %d — the same plan must rank the same", d, r, again[d])
			}
		}
	}

	// A missing organization must not change anybody's rank. The old counter
	// advanced inside the seeding loop, AFTER the orgsByDom guard, so a
	// company absent from a -limit run renumbered every domain behind it.
	// leadAssignRank never consults the installation, so it cannot.
	partial := leadAssignRank(leadPlan(domains...))
	for d, r := range full {
		if partial[d] != r {
			t.Errorf("%s ranked %d in the full plan and %d when an organization was missing", d, r, partial[d])
		}
	}
}

// TestNameLocaleForFollowsTheCompany pins which naming culture a company's
// generated lead is drawn from. The bug this replaces put a German name on a
// Korean and a Vietnamese company, four rows apart on the first page of the
// lead list.
func TestNameLocaleForFollowsTheCompany(t *testing.T) {
	for domain, want := range map[string]nameLocale{
		"gamsoft.kr":     namesKO,
		"condt.co.kr":    namesKO,
		"utp.or.kr":      namesKO,
		"dacell.com":     namesKO, // Korean company on a .com
		"hongikinfo.com": namesKO,
		"aubot.vn":       namesVI,
		"i-soft.com.vn":  namesVI,
	} {
		if got := nameLocaleFor(domain); got != want {
			t.Errorf("nameLocaleFor(%q) = %q, want %q", domain, got, want)
		}
	}
}

// TestGeneratedLeadNamesAreDistinct is the other half of the same bug: an 8x8
// German pool hashed by domain produced "Kilian Wenzel" nine times across 45
// leads. Names are now assigned by rank, so a pool bigger than the lead count
// cannot repeat.
func TestGeneratedLeadNamesAreDistinct(t *testing.T) {
	for _, culture := range []nameLocale{namesDE, namesVI, namesKO, namesEN} {
		pool := leadNamesByLocale[culture]
		seen := map[string]bool{}
		// One more than any single dataset draws from one culture.
		for rank := 0; rank < 16; rank++ {
			first, last := generatedLeadName("example."+string(culture), rank)
			name := first + " " + last
			if seen[name] {
				t.Errorf("%s: %q repeats within the first 16 ranks", culture, name)
			}
			seen[name] = true
		}
		if len(pool.First) < 16 || len(pool.Last) < 16 {
			t.Errorf("%s pool is too small to keep 16 leads distinct: %d first, %d last",
				culture, len(pool.First), len(pool.Last))
		}
	}
}

// TestFoldASCIIProducesAMailableLocalPart pins the folding. Without it a
// Vietnamese lead was handed thao.đỗ@example.com — combining marks and a
// D-with-stroke in an address, which no mail system produces.
func TestFoldASCIIProducesAMailableLocalPart(t *testing.T) {
	for in, want := range map[string]string{
		"Nguyễn":  "nguyen",
		"Đỗ":      "do",
		"Đặng":    "dang",
		"Thảo":    "thao",
		"Krüger":  "krueger",  // German expands rather than dropping the mark
		"Jüttner": "juettner", // matches the dataset's own synth_emails.py
		"Weiß":    "weiss",
		"Ji-woo":  "jiwoo",
		"Grønn":   "gronn",
		"Kessler": "kessler",
	} {
		if got := foldASCII(in); got != want {
			t.Errorf("foldASCII(%q) = %q, want %q", in, got, want)
		}
	}
}
