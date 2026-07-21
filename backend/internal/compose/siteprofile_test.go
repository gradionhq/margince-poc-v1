// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The profile gate's contract: the RESOLVER decides source_url (the
// model cannot name a page), verbatim-shaped fields demand their value
// in the cited passage, paraphrase fields keep the resolved passage as
// evidence with a warning-only overlap signal, and the legal trio still
// answers to the census gate.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// profileFixtureIndex numbers the pages IN GIVEN ORDER (home s0,
// impressum s1) — the rank sort belongs to profileExcerptPages, which
// has its own test.
func profileFixtureIndex() snippetIndex {
	return newSnippetIndex([]crawlPage{
		{
			URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome,
			Text: "Acme ships industrial robots and automation lines for manufacturers across Europe since 1998, with in-house engineering.",
		},
		{
			URL: seedURL + "/impressum", Kind: crmcontracts.SiteReadPageKindImpressum,
			Text: "Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. USt-ID DE123456789 nach Paragraf 27a UStG.",
		},
	})
}

func TestGateProfileResolverAssignsTheSourcePage(t *testing.T) {
	idx := profileFixtureIndex()
	reply := `{"fields":[
		{"f":"legal_name","v":"Acme Robotics GmbH","e":"s1","c":0.9},
		{"f":"value_proposition","v":"Industrial robots and automation lines for European manufacturers","e":"s0","c":0.85}]}`
	fields, dropped := gateProfile(reply, idx)
	if len(fields) != 2 {
		t.Fatalf("both fields should survive: %+v (dropped %+v)", fields, dropped)
	}
	byName := map[string]evidencedField{}
	for _, f := range fields {
		byName[f.Field] = f
	}
	if byName["legal_name"].SourceURL != seedURL+"/impressum" {
		t.Fatalf("the resolver must place legal_name on the imprint: %+v", byName["legal_name"])
	}
	if byName["value_proposition"].SourceURL != seedURL {
		t.Fatalf("the resolver must place the paraphrase on the home page: %+v", byName["value_proposition"])
	}
	if byName["value_proposition"].EvidenceSnippet == "" {
		t.Fatal("the paraphrase must carry the resolved passage as evidence")
	}
}

func TestGateProfileHardGateRefusesAnUncitedVerbatimField(t *testing.T) {
	idx := profileFixtureIndex()
	reply := `{"fields":[
		{"f":"legal_name","v":"Beispiel Holding AG","e":"s1","c":0.9},
		{"f":"display_name","v":"Acme","e":"s0","c":0.9}]}`
	fields, dropped := gateProfile(reply, idx)
	if len(fields) != 1 || fields[0].Field != "display_name" {
		t.Fatalf("the un-named legal_name must drop, display_name survives: %+v", fields)
	}
	if reasons := dropReasons(dropped); reasons["legal_name"] != dropValueNotInSnippet {
		t.Fatalf("want value_not_in_snippet for the invented legal name: %+v", dropped)
	}
}

func TestGateProfileParaphraseOverlapIsWarningOnly(t *testing.T) {
	idx := profileFixtureIndex()
	// A paraphrase sharing no ≥4-rune content word with its passage: the
	// field SURVIVES, the warning is recorded.
	reply := `{"fields":[{"f":"icp","v":"Fertigungsbetriebe in der DACH-Region","e":"s0","c":0.8}]}`
	fields, dropped := gateProfile(reply, idx)
	if len(fields) != 1 {
		t.Fatalf("a low-overlap paraphrase must survive: %+v", fields)
	}
	if reasons := dropReasons(dropped); reasons["icp"] != dropParaphraseLowOverlap {
		t.Fatalf("the low overlap must be recorded as a warning: %+v", dropped)
	}
}

func TestGateProfileRefusesUnknownIdsAndBadConfidence(t *testing.T) {
	idx := profileFixtureIndex()
	reply := `{"fields":[
		{"f":"industry","v":"Robotics","e":"s99","c":0.9},
		{"f":"usp","v":"In-house engineering","e":"s0","c":1.7}]}`
	fields, dropped := gateProfile(reply, idx)
	if len(fields) != 0 {
		t.Fatalf("nothing here may survive: %+v", fields)
	}
	reasons := dropReasons(dropped)
	if reasons["industry"] != dropSnippetIDUnknown || reasons["usp"] != dropConfidenceRange {
		t.Fatalf("drops = %+v", dropped)
	}
}

func TestProfileExcerptPagesBoundLegalPagesAndReserveCommercialEvidence(t *testing.T) {
	var pages []crawlPage
	for i := 0; i < 8; i++ {
		pages = append(pages, crawlPage{
			URL: seedURL + "/about" + string(rune('a'+i)), Kind: crmcontracts.SiteReadPageKindAbout,
			Text: string(make([]byte, 0)) + string(bytesOfRunes('a', 9000)),
		})
	}
	for i := 0; i < 6; i++ {
		pages = append(pages, crawlPage{
			URL: seedURL + "/legal" + string(rune('a'+i)), Kind: crmcontracts.SiteReadPageKindImpressum,
			Text: string(bytesOfRunes('l', 9000)),
		})
	}
	excerpts := profileExcerptPages(pages)
	imprints := 0
	commercial := 0
	total := 0
	for _, page := range excerpts {
		total += len([]rune(page.Text))
		if page.Kind == crmcontracts.SiteReadPageKindImpressum {
			imprints++
		} else {
			commercial++
		}
	}
	if imprints != profileMaxImpressumPages {
		t.Fatalf("legal excerpts = %d, want bounded share %d", imprints, profileMaxImpressumPages)
	}
	if commercial < 3 {
		t.Fatalf("the profile must retain a useful commercial cross-section, got %d pages", commercial)
	}
	if total > profileExcerptBudgetRunes {
		t.Fatalf("all excerpts exceed the budget: %d runes", total)
	}
}

func bytesOfRunes(r byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = r
	}
	return out
}
