// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The corpus gate's contract — every no-guess rule the per-page gates
// enforced, restated over the one-call reply: evidence must be on the
// NAMED page (no cross-page laundering), vocabularies are closed,
// people are published-only, and the legal-entity census drives the
// multi-entity abstention.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func corpusFixtureChunk() corpusChunk {
	return corpusChunk{pages: []crawlPage{
		{
			URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome,
			Text: "Acme ships robots since 1998. Offices in Stuttgart and Hanoi. Built on SuperPLC.",
		},
		{
			URL: seedURL + "/impressum", Kind: crmcontracts.SiteReadPageKindImpressum,
			Text: "Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart.",
		},
		{
			URL: seedURL + "/team", Kind: crmcontracts.SiteReadPageKindTeam,
			Text: "Anna Muster is our Chief Executive Officer. Reach her at anna@acme.example.",
		},
	}}
}

func dropReasonsByField(dropped []droppedFinding) map[string]string {
	out := map[string]string{}
	for _, d := range dropped {
		out[d.Field] = d.Reason
	}
	return out
}

func TestGateCorpusEvidenceMustBeOnTheNamedPage(t *testing.T) {
	// Both claims quote real site text — but the second names the WRONG
	// page for its quote: cross-page laundering must drop.
	reply := `{"fields":[
		{"field":"display_name","value":"Acme","evidence_snippet":"Acme ships robots","source_url":"` + seedURL + `","confidence":0.9},
		{"field":"history","value":"Since 1998","evidence_snippet":"since 1998","source_url":"` + seedURL + `/impressum","confidence":0.9}],
		"facts":[],"people":[],"legal_entities":[]}`
	res, dropped := gateCorpus(reply, corpusFixtureChunk())
	if len(res.fields) != 1 || res.fields[0].Field != "display_name" {
		t.Fatalf("only the correctly-attributed field may survive: %+v", res.fields)
	}
	if reasons := dropReasonsByField(dropped); reasons["history"] != dropEvidenceNotOnPage {
		t.Fatalf("the laundered quote must drop as evidence_not_on_page: %+v", dropped)
	}
}

func TestGateCorpusRefusesUnknownPagesAndVocabularyAndBadConfidence(t *testing.T) {
	reply := `{"fields":[
		{"field":"display_name","value":"Acme","evidence_snippet":"Acme ships robots","source_url":"https://elsewhere.example","confidence":0.9},
		{"field":"not_a_field","value":"x","evidence_snippet":"Acme ships robots","source_url":"` + seedURL + `","confidence":0.9}],
		"facts":[
		{"category":"offering","field":"founded_year","value":"1998","evidence_snippet":"since 1998","source_url":"` + seedURL + `","confidence":0.9},
		{"category":"signal","field":"technology","value":"SuperPLC — control platform","evidence_snippet":"Built on SuperPLC","source_url":"` + seedURL + `","confidence":1.7}],
		"people":[],"legal_entities":[]}`
	res, dropped := gateCorpus(reply, corpusFixtureChunk())
	if len(res.fields)+len(res.facts) != 0 {
		t.Fatalf("nothing here may survive: %+v", res)
	}
	reasons := dropReasonsByField(dropped)
	want := map[string]string{
		"display_name": dropUnknownPage,
		"not_a_field":  dropUnknownField,
		"founded_year": dropUnknownField, // company field claimed under offering
		"technology":   dropConfidenceRange,
	}
	for field, reason := range want {
		if reasons[field] != reason {
			t.Fatalf("field %s dropped for %q, want %q (%+v)", field, reasons[field], reason, dropped)
		}
	}
}

func TestGateCorpusKeepsTheNewVocabularyWithValueKeys(t *testing.T) {
	reply := `{"fields":[],"facts":[
		{"category":"company","field":"location","value":"Stuttgart","evidence_snippet":"Offices in Stuttgart and Hanoi","source_url":"` + seedURL + `","confidence":0.9},
		{"category":"company","field":"location","value":"Hanoi","evidence_snippet":"Offices in Stuttgart and Hanoi","source_url":"` + seedURL + `","confidence":0.9},
		{"category":"signal","field":"technology","value":"SuperPLC — control platform","evidence_snippet":"Built on SuperPLC","source_url":"` + seedURL + `","confidence":0.8}],
		"people":[],"legal_entities":[]}`
	res, dropped := gateCorpus(reply, corpusFixtureChunk())
	if len(res.facts) != 3 {
		t.Fatalf("all three v2-vocabulary facts must survive: %+v (dropped %+v)", res.facts, dropped)
	}
	for _, fact := range res.facts {
		if fact.ValueKey == "" {
			t.Fatalf("multi-value fact without a value_key: %+v", fact)
		}
	}
}

func TestGateCorpusPeopleStayPublishedOnly(t *testing.T) {
	teamURL := seedURL + "/team"
	reply := `{"fields":[],"facts":[],"people":[
		{"name":"Anna Muster","role":"Chief Executive Officer","published_email":"anna@acme.example","linkedin_url":"https://linkedin.com/in/anna",
		 "evidence_snippet":"Anna Muster is our Chief Executive Officer","source_url":"` + teamURL + `","confidence":0.9},
		{"name":"Carla Invented","role":"CTO","evidence_snippet":"Anna Muster is our Chief Executive Officer","source_url":"` + teamURL + `","confidence":0.9}],
		"legal_entities":[]}`
	res, dropped := gateCorpus(reply, corpusFixtureChunk())
	if len(res.people) != 1 || res.people[0].Name != "Anna Muster" {
		t.Fatalf("only the published person may survive: %+v", res.people)
	}
	if res.people[0].PublishedEmail != "anna@acme.example" {
		t.Fatalf("the printed email must survive verbatim: %+v", res.people[0])
	}
	if res.people[0].LinkedinURL != "" {
		t.Fatalf("a LinkedIn URL the page never prints must be stripped: %+v", res.people[0])
	}
	if reasons := dropReasonsByField(dropped); reasons["Carla Invented"] != dropNameRoleUnlinked {
		t.Fatalf("the invented person must drop as name_role_not_in_snippet: %+v", dropped)
	}
}

func TestApplyLegalGateAbstainsOnDisagreeingEntitiesAndKeepsTheRest(t *testing.T) {
	chunk := corpusFixtureChunk()
	idx := indexCorpusPages(chunk)
	res := corpusResult{
		fields: []evidencedField{
			{Field: "display_name", Value: "Acme", EvidenceSnippet: "Acme ships robots", SourceURL: seedURL, Confidence: 0.9},
			{Field: "legal_name", Value: "Acme Robotics GmbH", EvidenceSnippet: "Acme Robotics GmbH", SourceURL: seedURL + "/impressum", Confidence: 0.9},
		},
		legalEntities: []corpusLegalEntity{
			{Name: "Acme Robotics GmbH", SourceURL: seedURL + "/impressum"},
			{Name: "Beispiel Holding AG", SourceURL: seedURL + "/impressum"},
		},
	}
	// The census gate itself refuses an entity name the page never
	// prints — a hallucinated entity cannot force a false abstention.
	gated, _ := gateCorpus(`{"fields":[],"facts":[],"people":[],"legal_entities":[
		{"name":"Acme Robotics GmbH","source_url":"`+seedURL+`/impressum"},
		{"name":"Beispiel Holding AG","source_url":"`+seedURL+`/impressum"}]}`, chunk)
	if len(gated.legalEntities) != 1 {
		t.Fatalf("a hallucinated entity must not pass the census gate: %+v", gated.legalEntities)
	}

	fields, conflict, dropped := applyLegalGate(res, idx)
	if !conflict {
		t.Fatal("two distinct entities must flag the multi-entity domain")
	}
	if len(fields) != 1 || fields[0].Field != "display_name" {
		t.Fatalf("the abstention must strip the legal trio and keep the rest: %+v", fields)
	}
	if reasons := dropReasonsByField(dropped); reasons["legal_name"] != dropLegalConflict {
		t.Fatalf("the stripped trio must be recorded as legal_conflict drops: %+v", dropped)
	}
}

func TestApplyLegalGateSingleEntityTrioOnlyFromALegalPage(t *testing.T) {
	chunk := corpusFixtureChunk()
	idx := indexCorpusPages(chunk)
	res := corpusResult{
		fields: []evidencedField{
			{Field: "legal_name", Value: "Acme Robotics GmbH", EvidenceSnippet: "Acme Robotics GmbH", SourceURL: seedURL + "/impressum", Confidence: 0.9},
			{Field: "registered_address", Value: "Somewhere else", EvidenceSnippet: "Acme ships robots", SourceURL: seedURL, Confidence: 0.9},
		},
		legalEntities: []corpusLegalEntity{{Name: "Acme Robotics GmbH", SourceURL: seedURL + "/impressum"}},
	}
	fields, conflict, dropped := applyLegalGate(res, idx)
	if conflict {
		t.Fatal("one entity is no conflict")
	}
	if len(fields) != 1 || fields[0].Field != "legal_name" {
		t.Fatalf("the legal-page-quoted trio field survives, the marketing-page one drops: %+v", fields)
	}
	if reasons := dropReasonsByField(dropped); reasons["registered_address"] != dropLegalNotFromLegalPage {
		t.Fatalf("the marketing-page trio claim must drop as legal_field_not_from_legal_page: %+v", dropped)
	}
}

func TestGateCorpusLegalCensusRefusesDeepAndNonLegalPages(t *testing.T) {
	chunk := corpusChunk{pages: []crawlPage{
		{URL: seedURL + "/about", Kind: crmcontracts.SiteReadPageKindAbout, Text: "Acme Robotics GmbH does robots."},
		{URL: seedURL + "/customers/other/legal", Kind: crmcontracts.SiteReadPageKindImpressum, Text: "Other Co AG imprint."},
	}}
	reply := `{"fields":[],"facts":[],"people":[],"legal_entities":[
		{"name":"Acme Robotics GmbH","source_url":"` + seedURL + `/about"},
		{"name":"Other Co AG","source_url":"` + seedURL + `/customers/other/legal"}]}`
	res, dropped := gateCorpus(reply, chunk)
	if len(res.legalEntities) != 0 {
		t.Fatalf("non-legal and deep legal pages cannot testify to identity: %+v", res.legalEntities)
	}
	for _, d := range dropped {
		if d.Reason != dropLegalNotFromLegalPage {
			t.Fatalf("drop reason = %q, want legal_field_not_from_legal_page (%+v)", d.Reason, dropped)
		}
	}
}

func TestGateCorpusNormalizedEvidenceSurvivesTypography(t *testing.T) {
	chunk := corpusChunk{pages: []crawlPage{
		{URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome, Text: "Wir sind die „Acme Robotics“ – Ihr Partner."},
	}}
	reply := `{"fields":[
		{"field":"display_name","value":"Acme Robotics","evidence_snippet":"die \"Acme Robotics\" - Ihr Partner","source_url":"` + seedURL + `","confidence":0.9}],
		"facts":[],"people":[],"legal_entities":[]}`
	res, dropped := gateCorpus(reply, chunk)
	if len(res.fields) != 1 {
		t.Fatalf("a faithfully-quoted snippet with normalized typography must survive: %+v", dropped)
	}
}

func TestMergeChunkResultsIsDeterministicFirstChunkWinsFields(t *testing.T) {
	a := corpusResult{
		fields:        []evidencedField{{Field: "display_name", Value: "Acme", Confidence: 0.8}},
		facts:         []people.DeepReadFact{{Category: "offering", Field: "service", Value: "Robots — assembly", ValueKey: "robots", Confidence: 0.6}},
		people:        []sitePerson{{Name: "Anna Muster", Role: "CEO", Confidence: 0.6}},
		legalEntities: []corpusLegalEntity{{Name: "Acme Robotics GmbH"}},
	}
	b := corpusResult{
		fields:        []evidencedField{{Field: "display_name", Value: "ACME Corp", Confidence: 0.95}},
		facts:         []people.DeepReadFact{{Category: "offering", Field: "service", Value: "Robots — assembly line", ValueKey: "robots", Confidence: 0.9}},
		people:        []sitePerson{{Name: "anna  muster", Role: "Chief Executive Officer", Confidence: 0.9}},
		legalEntities: []corpusLegalEntity{{Name: "Beispiel Holding AG"}},
	}
	merged := mergeChunkResults([]corpusResult{a, b})
	if len(merged.fields) != 1 || merged.fields[0].Value != "Acme" {
		t.Fatalf("first chunk must win a contested field: %+v", merged.fields)
	}
	if len(merged.facts) != 1 || merged.facts[0].Confidence != 0.9 {
		t.Fatalf("higher confidence must win a value_key-equal fact: %+v", merged.facts)
	}
	if len(merged.people) != 1 || merged.people[0].Role != "Chief Executive Officer" {
		t.Fatalf("higher confidence must win a normalized-name-equal person: %+v", merged.people)
	}
	if len(merged.legalEntities) != 2 {
		t.Fatalf("legal entities union across chunks: %+v", merged.legalEntities)
	}
}
