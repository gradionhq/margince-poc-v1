// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The page-fact gate's contract — the no-guess rules restated over
// snippet citations: closed vocabulary, the value's name in the cited
// passage, people published-only, entities only from shallow legal
// pages, and every refusal recorded with its reason.

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func pageFixture(kind crmcontracts.SiteReadPageKind, url, text string) (crawlPage, pageMenu, snippetIndex) {
	page := crawlPage{URL: url, Kind: kind, Text: text}
	menu, ok := menuForKind(kind)
	if !ok {
		panic("fixture kind has no menu")
	}
	return page, menu, newSnippetIndex([]crawlPage{page})
}

func dropReasons(dropped []droppedFinding) map[string]string {
	out := map[string]string{}
	for _, d := range dropped {
		out[d.Field] = d.Reason
	}
	return out
}

func TestFactFieldNamesAreGloballyUniqueAcrossCategories(t *testing.T) {
	// The compact reply names no category — the field implies it, which
	// only works while no field name appears in two categories.
	seen := map[string]string{}
	for category, fields := range people.OrganizationFactFields {
		for _, field := range fields {
			if prior, dup := seen[field]; dup {
				t.Fatalf("fact field %q lives in both %s and %s — the category inference breaks", field, prior, category)
			}
			seen[field] = category
		}
	}
}

func TestMenuForKindRoutesFactBearingKindsOnly(t *testing.T) {
	if _, ok := menuForKind(crmcontracts.SiteReadPageKindOther); ok {
		t.Fatal("unclassified pages must make no call")
	}
	menu, ok := menuForKind(crmcontracts.SiteReadPageKindImpressum)
	if !ok || !menu.entities || menu.people {
		t.Fatalf("impressum menu = %+v, want company fields + entities", menu)
	}
	menu, ok = menuForKind(crmcontracts.SiteReadPageKindServices)
	if !ok || menu.entities || menu.people {
		t.Fatalf("services menu = %+v, want offering fields only", menu)
	}
	found := false
	for _, f := range menu.factFields {
		if f == "technology" {
			found = true
		}
	}
	if !found {
		t.Fatal("catalog pages must be allowed to name technologies")
	}
}

func TestGatePageFactsDemandsTheNameInTheCitedPassage(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindServices, seedURL+"/services",
		"Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute, storage and networking budgets.")
	reply := `{"facts":[
		{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"},
		{"f":"service","v":"Phishing Simulation — never on this page","e":"s0"},
		{"f":"founded_year","v":"1998","e":"s0"},
		{"f":"service","v":"","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.facts) != 1 || factName(res.facts[0].Value) != "Cloud Cost Audit" {
		t.Fatalf("only the cited-and-named service may survive: %+v", res.facts)
	}
	// The stored evidence is the resolved passage and carries the name
	// (the adjacent-join recovery has its own proof in sitesnippet_test).
	if !strings.Contains(res.facts[0].EvidenceSnippet, "Cloud Cost Audit") {
		t.Fatalf("evidence must carry the item name: %q", res.facts[0].EvidenceSnippet)
	}
	if res.facts[0].Confidence != gatedConfidence {
		t.Fatalf("reference-evidence facts carry the fixed gate confidence, got %v", res.facts[0].Confidence)
	}
	byReason := map[string]int{}
	for _, d := range dropped {
		byReason[d.Reason]++
	}
	if byReason[dropValueNotInSnippet] != 1 || byReason[dropEmptyValue] != 1 || byReason[dropUnknownField] != 1 {
		t.Fatalf("drops = %+v, want one uncited service, one empty value, one off-menu field", dropped)
	}
}

func TestGatePageFactsPeopleStayPublishedOnly(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindAbout, seedURL+"/about",
		"Anna Muster is our Chief Executive Officer and founded the automation practice. Reach her at anna@acme.example for partnership topics.")
	reply := `{"facts":[],"people":[
		{"n":"Anna Muster","r":"Chief Executive Officer","m":"anna@acme.example","l":"https://linkedin.com/in/anna","e":"s0"},
		{"n":"Carla Invented","r":"CTO","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.people) != 1 || res.people[0].Name != "Anna Muster" {
		t.Fatalf("only the published person may survive: %+v", res.people)
	}
	if res.people[0].PublishedEmail != "anna@acme.example" || res.people[0].LinkedinURL != "" {
		t.Fatalf("printed email kept, unprinted linkedin stripped: %+v", res.people[0])
	}
	if reasons := dropReasons(dropped); reasons["Carla Invented"] != dropValueNotInSnippet {
		t.Fatalf("the invented person must drop: %+v", dropped)
	}
}

func TestGatePageFactsEntitiesOnlyFromShallowLegalPages(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/impressum",
		"Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. Registergericht Stuttgart HRB 12345, USt-ID DE123456789.")
	reply := `{"facts":[],"entities":[
		{"n":"Acme Robotics GmbH","e":"s0"},
		{"n":"Hallucinated Holding AG","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.entities) != 1 || res.entities[0].Name != "Acme Robotics GmbH" {
		t.Fatalf("only the named entity may pass the census: %+v", res.entities)
	}
	if reasons := dropReasons(dropped); reasons["Hallucinated Holding AG"] != dropValueNotInSnippet {
		t.Fatalf("a hallucinated entity must drop: %+v", dropped)
	}

	// A deep legal path never testifies, whatever it names.
	deepPage := crawlPage{
		URL: seedURL + "/customers/other/legal", Kind: crmcontracts.SiteReadPageKindImpressum,
		Text: "Other Co AG imprint for a customer project hosted under a deep path with plenty of text.",
	}
	deepIdx := newSnippetIndex([]crawlPage{deepPage})
	res, dropped = gatePageFacts(`{"facts":[],"entities":[{"n":"Other Co AG","e":"s0"}]}`, deepPage, menu, deepIdx)
	if len(res.entities) != 0 {
		t.Fatalf("a deep legal page testified: %+v", res.entities)
	}
	if reasons := dropReasons(dropped); reasons["Other Co AG"] != dropLegalNotFromLegalPage {
		t.Fatalf("want legal_field_not_from_legal_page: %+v", dropped)
	}
}

func TestGatePageFactsValueKeysAndDuplicates(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindHome, seedURL,
		"Offices in Stuttgart and Hanoi serve industrial customers across Europe and Asia with automation projects.")
	reply := `{"facts":[
		{"f":"location","v":"Stuttgart","e":"s0"},
		{"f":"location","v":"Hanoi","e":"s0"},
		{"f":"location","v":"Stuttgart","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.facts) != 2 {
		t.Fatalf("two distinct locations survive, the repeat drops: %+v", res.facts)
	}
	for _, f := range res.facts {
		if f.ValueKey == "" {
			t.Fatalf("multi-value fact without value_key: %+v", f)
		}
	}
	dupSeen := false
	for _, d := range dropped {
		if d.Reason == dropDuplicate {
			dupSeen = true
		}
	}
	if !dupSeen {
		t.Fatalf("the repeated location left no duplicate drop: %+v", dropped)
	}
}
