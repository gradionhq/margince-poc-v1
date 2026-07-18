// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The normalized evidence match: presentation differences (quotes,
// dashes, whitespace, case, soft hyphens) are forgiven; changed or
// invented words never are — the no-guess gate keeps its teeth.

import (
	"testing"
)

func TestEvidenceOnPageForgivesPresentationNeverWords(t *testing.T) {
	page := "Die Firma „Acme – Robotics“ GmbH liefert Automatisierung.\nSeit 1998."
	pageNorm := normalizeEvidence(page)
	cases := []struct {
		name    string
		snippet string
		want    bool
	}{
		{"byte-exact match", "Acme – Robotics", true},
		{"straightened quotes and dash", `Die Firma "Acme - Robotics" GmbH`, true},
		{"nbsp reflowed to plain space", "liefert Automatisierung.", true},
		{"case folded", "die firma", true},
		{"line break collapsed", "Automatisierung. Seit 1998.", true},
		{"reworded snippet still fails", "Die Firma Acme Robotics GmbH", false},
		{"invented snippet still fails", "gegründet in Stuttgart", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := evidenceOnPage(page, pageNorm, tc.snippet); got != tc.want {
				t.Fatalf("evidenceOnPage(%q) = %v, want %v", tc.snippet, got, tc.want)
			}
		})
	}
}

func TestGateEvidenceKeepsNormalizedQuotesAndReportsEveryDropReason(t *testing.T) {
	page := "Wir sind die „Acme Robotics“ – Ihr Partner für Automatisierung seit 1998."
	reply := `{"fields":[
		{"field":"display_name","value":"Acme Robotics","evidence_snippet":"die \"Acme Robotics\" - Ihr Partner","confidence":0.9},
		{"field":"history","value":"Seit 1998","evidence_snippet":"invented words entirely","confidence":0.9},
		{"field":"icp","value":"","evidence_snippet":"Ihr Partner","confidence":0.9},
		{"field":"not_a_field","value":"x","evidence_snippet":"Ihr Partner","confidence":0.9},
		{"field":"usp","value":"Partner","evidence_snippet":"Ihr Partner für Automatisierung","confidence":1.5}]}`

	fields, dropped := gateEvidence(reply, page, "https://acme.example", coldStartFieldValid)

	if len(fields) != 1 || fields[0].Field != "display_name" {
		t.Fatalf("the normalized-quote snippet should be the one survivor, got %+v", fields)
	}
	reasons := map[string]string{}
	for _, d := range dropped {
		reasons[d.Field] = d.Reason
	}
	want := map[string]string{
		"history":     dropEvidenceNotOnPage,
		"icp":         dropEmptyValue,
		"not_a_field": dropUnknownField,
		"usp":         dropConfidenceRange,
	}
	for field, reason := range want {
		if reasons[field] != reason {
			t.Fatalf("field %s dropped for %q, want %q (all: %+v)", field, reasons[field], reason, dropped)
		}
	}
}

func TestGateCategoryFactsNormalizedEvidenceSurvives(t *testing.T) {
	page := "Zertifiziert nach ISO 27001 — seit 2019."
	reply := `{"fields":[
		{"field":"certification","value":"ISO 27001","evidence_snippet":"Zertifiziert nach ISO 27001 - seit 2019","confidence":0.9}]}`
	facts, dropped := gateCategoryFacts(reply, page, "https://acme.example/about", "signal")
	if len(facts) != 1 || facts[0].Field != "certification" {
		t.Fatalf("the NBSP/em-dash snippet should survive normalized, got %+v (dropped %+v)", facts, dropped)
	}
}

func TestGateTeamPeopleNormalizedNameRoleAssociationSurvives(t *testing.T) {
	page := "Anna Muster – Chief Executive Officer of Acme."
	reply := `{"people":[{"name":"Anna Muster","role":"Chief Executive Officer",
		"evidence_snippet":"Anna Muster - Chief Executive Officer","confidence":0.9}]}`
	persons, dropped := gateTeamPeople(reply, page, "https://acme.example/team")
	if len(persons) != 1 || persons[0].Name != "Anna Muster" {
		t.Fatalf("the NBSP-spaced person should survive normalized, got %+v (dropped %+v)", persons, dropped)
	}
}
