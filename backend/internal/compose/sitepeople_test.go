// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The people gate's published-only contract (R5, NEVER-8): a person
// survives only when name AND role are verbatim on the page; a contact
// detail the page does not print is stripped while the person survives —
// never fabricated, never enriched from elsewhere.

import (
	"reflect"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// teamPageText is the fixture team page: Anna with a printed email and
// LinkedIn URL, Bernd with neither.
const teamPageText = "Our team. Anna Muster is our Chief Executive Officer. " +
	"Reach her at anna@acme.example or https://linkedin.com/in/anna-muster. " +
	"Bernd Beispiel leads sales as Head of Sales."

func TestGateTeamPeopleKeepsOnlyVerbatimEvidencedPeople(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  []sitePerson
	}{
		{
			name: "published email and linkedin survive verbatim",
			reply: `{"people":[{"name":"Anna Muster","role":"Chief Executive Officer",
				"published_email":"anna@acme.example","linkedin_url":"https://linkedin.com/in/anna-muster",
				"evidence_snippet":"Anna Muster is our Chief Executive Officer","confidence":0.9}]}`,
			want: []sitePerson{{
				Name: "Anna Muster", Role: "Chief Executive Officer",
				PublishedEmail: "anna@acme.example", LinkedinURL: "https://linkedin.com/in/anna-muster",
				EvidenceSnippet: "Anna Muster is our Chief Executive Officer",
				SourceURL:       "https://acme.example/team", Confidence: 0.9,
			}},
		},
		{
			name: "an unpublished email is stripped while the person survives",
			reply: `{"people":[{"name":"Bernd Beispiel","role":"Head of Sales",
				"published_email":"bernd@acme.example","linkedin_url":"https://linkedin.com/in/bernd",
				"evidence_snippet":"Bernd Beispiel leads sales as Head of Sales","confidence":0.8}]}`,
			want: []sitePerson{{
				Name: "Bernd Beispiel", Role: "Head of Sales",
				EvidenceSnippet: "Bernd Beispiel leads sales as Head of Sales",
				SourceURL:       "https://acme.example/team", Confidence: 0.8,
			}},
		},
		{
			name: "a name the page never prints is dropped",
			reply: `{"people":[{"name":"Carla Invented","role":"Head of Sales",
				"evidence_snippet":"Bernd Beispiel leads sales as Head of Sales","confidence":0.9}]}`,
			want: nil,
		},
		{
			name: "a role the page never prints is dropped",
			reply: `{"people":[{"name":"Bernd Beispiel","role":"Chief Revenue Officer",
				"evidence_snippet":"Bernd Beispiel leads sales as Head of Sales","confidence":0.9}]}`,
			want: nil,
		},
		{
			name: "a non-verbatim evidence snippet is dropped",
			reply: `{"people":[{"name":"Anna Muster","role":"Chief Executive Officer",
				"evidence_snippet":"nowhere on this page","confidence":0.9}]}`,
			want: nil,
		},
		{
			name: "an out-of-range confidence is dropped",
			reply: `{"people":[{"name":"Anna Muster","role":"Chief Executive Officer",
				"evidence_snippet":"Anna Muster is our Chief Executive Officer","confidence":1.5}]}`,
			want: nil,
		},
		{
			name: "duplicate names dedupe on the normalized spelling, higher confidence winning",
			reply: `{"people":[
				{"name":"Anna Muster","role":"Chief Executive Officer",
				 "evidence_snippet":"Anna Muster is our Chief Executive Officer","confidence":0.6},
				{"name":" Anna Muster ","role":"Chief Executive Officer",
				 "published_email":"anna@acme.example",
				 "evidence_snippet":"Anna Muster is our Chief Executive Officer","confidence":0.9}]}`,
			want: []sitePerson{{
				Name: "Anna Muster", Role: "Chief Executive Officer",
				PublishedEmail:  "anna@acme.example",
				EvidenceSnippet: "Anna Muster is our Chief Executive Officer",
				SourceURL:       "https://acme.example/team", Confidence: 0.9,
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gateTeamPeople(tc.reply, teamPageText, "https://acme.example/team")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("gateTeamPeople = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGateTeamPeopleRefusesUnparseableOutput(t *testing.T) {
	if got := gateTeamPeople("not json at all", teamPageText, "https://acme.example/team"); got != nil {
		t.Fatalf("unparseable model output yielded %+v, want nothing", got)
	}
}

func TestMergeTeamPeopleDedupesAcrossPagesOnNormalizedName(t *testing.T) {
	pages := []pageFields{
		{kind: crmcontracts.SiteReadPageKindTeam, people: []sitePerson{
			{Name: "Anna Muster", Role: "CEO", SourceURL: "https://acme.example/team", Confidence: 0.6},
			{Name: "Bernd Beispiel", Role: "Head of Sales", SourceURL: "https://acme.example/team", Confidence: 0.8},
		}},
		{kind: crmcontracts.SiteReadPageKindTeam, people: []sitePerson{
			{Name: "anna  muster", Role: "Chief Executive Officer", SourceURL: "https://acme.example/about-team", Confidence: 0.9},
		}},
	}
	got := mergeTeamPeople(pages)
	if len(got) != 2 {
		t.Fatalf("merged %d people, want 2 (Anna deduped across pages)", len(got))
	}
	if got[0].Role != "Chief Executive Officer" || got[0].Confidence != 0.9 {
		t.Fatalf("Anna = %+v, want the higher-confidence second-page entry under first-seen order", got[0])
	}
	if got[1].Name != "Bernd Beispiel" {
		t.Fatalf("second merged person = %+v, want Bernd", got[1])
	}
}

func TestSiteLeadSourceIDIsStableAcrossReReadsAndNameReflow(t *testing.T) {
	a := siteLeadSourceID("https://acme.example/team", "Anna Muster")
	b := siteLeadSourceID("https://acme.example/team", "  anna   MUSTER ")
	if a != b {
		t.Fatal("the lead natural key changed on a whitespace/case reflow of the same printed name")
	}
	if a == siteLeadSourceID("https://acme.example/team", "Bernd Beispiel") {
		t.Fatal("two different people share one lead natural key")
	}
	if strings.Contains(a, "@") || len(a) != 64 {
		t.Fatalf("source id = %q, want a bare sha256 hex digest (no PII in the key)", a)
	}
}
