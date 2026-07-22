// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func TestEntityClarifiesDetectAmbiguousLegalIdentity(t *testing.T) {
	entity := func(name, address string) people.SiteReadLegalEntity {
		return people.SiteReadLegalEntity{
			Name: name, RegisteredAddress: address,
			EvidenceSnippet: "Impressum: " + name, SourceURL: "https://acme.example/impressum",
		}
	}
	tests := map[string]struct {
		entities   []people.SiteReadLegalEntity
		wantFields []string
	}{
		"single entity is unambiguous": {
			entities: []people.SiteReadLegalEntity{entity("Acme GmbH", "Berlin 1")},
		},
		"two entities with two addresses ask both questions": {
			entities:   []people.SiteReadLegalEntity{entity("Acme GmbH", "Berlin 1"), entity("Acme Holding AG", "Zug 2")},
			wantFields: []string{fieldLegalName, fieldRegisteredAddress},
		},
		"two entities sharing one address ask only for the name": {
			entities:   []people.SiteReadLegalEntity{entity("Acme GmbH", "Berlin 1"), entity("Acme Holding AG", "Berlin 1")},
			wantFields: []string{fieldLegalName},
		},
		"duplicate census rows collapse to nothing": {
			entities: []people.SiteReadLegalEntity{entity("Acme GmbH", "Berlin 1"), entity("Acme GmbH", "Berlin 1")},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			read := people.SiteRead{DraftVersion: 4, LegalEntities: tc.entities}
			got := onboardingClarifies(read, nil, "en")
			if len(got) != len(tc.wantFields) {
				t.Fatalf("clarifies = %+v, want fields %v", got, tc.wantFields)
			}
			for i, field := range tc.wantFields {
				if got[i].Field != field || got[i].Id != "clarify:"+field+":4" {
					t.Fatalf("clarify %d = %+v, want field %q with a version-stable id", i, got[i], field)
				}
			}
		})
	}
}

func TestEntityClarifyOptionsCarryTheExactPrintedStringsAndEvidence(t *testing.T) {
	read := people.SiteRead{DraftVersion: 1, LegalEntities: []people.SiteReadLegalEntity{
		{Name: "Acme GmbH", RegisteredAddress: "Musterstraße 1, Berlin", EvidenceSnippet: "Acme GmbH, Musterstraße 1", SourceURL: "https://acme.example/impressum"},
		{Name: "Acme Holding AG", RegisteredAddress: "Bahnhofstrasse 2, Zug", SourceURL: "https://acme.example/legal"},
	}}
	clarifies := onboardingClarifies(read, nil, "en")
	if len(clarifies) != 2 {
		t.Fatalf("clarifies = %+v", clarifies)
	}
	names := clarifies[0]
	if names.Options[0].Value != "Acme GmbH" || names.Options[1].Value != "Acme Holding AG" {
		t.Fatalf("name option values = %+v", names.Options)
	}
	if names.Options[0].EvidenceUrl == nil || *names.Options[0].EvidenceUrl != "https://acme.example/impressum" ||
		names.Options[0].EvidenceSnippet == nil || *names.Options[0].EvidenceSnippet != "Acme GmbH, Musterstraße 1" {
		t.Fatalf("name option evidence = %+v", names.Options[0])
	}
	if names.Options[1].EvidenceSnippet != nil {
		t.Fatalf("an entity without a printed snippet must not gain one: %+v", names.Options[1])
	}
	addresses := clarifies[1]
	if addresses.Options[0].Value != "Musterstraße 1, Berlin" || addresses.Options[1].Value != "Bahnhofstrasse 2, Zug" {
		t.Fatalf("address option values = %+v", addresses.Options)
	}
	if addresses.Options[0].Detail == nil || *addresses.Options[0].Detail != "Acme GmbH" {
		t.Fatalf("address option detail should name the entity: %+v", addresses.Options[0])
	}
}

func TestEntityClarifyOptionsAreCappedAtTheContractLimit(t *testing.T) {
	entities := make([]people.SiteReadLegalEntity, 0, 9)
	for _, name := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
		entities = append(entities, people.SiteReadLegalEntity{Name: name + " GmbH", SourceURL: "https://acme.example/impressum"})
	}
	clarifies := onboardingClarifies(people.SiteRead{LegalEntities: entities}, nil, "en")
	if len(clarifies) != 1 || len(clarifies[0].Options) != onboardingClarifyOptionLimit {
		t.Fatalf("clarifies = %+v", clarifies)
	}
}

func TestConflictClarifiesMapOntoTheResolutionContract(t *testing.T) {
	current, source := "Acme Software", "human"
	comparisons := []people.SiteReadComparison{
		{Key: "display_name", ValueKind: "profile_field", Classification: "human_conflict", CurrentValue: &current, CurrentSource: &source, ProposedValue: "Acme GmbH"},
		{Key: "industry", ValueKind: "profile_field", Classification: "machine_change", CurrentValue: &current, ProposedValue: "Software"},
		{Key: "icp", ValueKind: "profile_field", Classification: "new", ProposedValue: "Mid-market"},
	}
	clarifies := onboardingClarifies(people.SiteRead{DraftVersion: 2}, comparisons, "en")
	if len(clarifies) != 1 {
		t.Fatalf("only the human conflict may clarify, got %+v", clarifies)
	}
	conflict := clarifies[0]
	if conflict.Id != "clarify:display_name:2" || conflict.Field != "display_name" ||
		conflict.AllowFreeText == nil || !*conflict.AllowFreeText || len(conflict.Options) != 2 {
		t.Fatalf("conflict clarify = %+v", conflict)
	}
	// Option one is keep_current, option two accept_proposal — the values
	// are the exact stored strings the resolution contract will receive.
	if conflict.Options[0].Value != current || conflict.Options[1].Value != "Acme GmbH" {
		t.Fatalf("conflict option values = %+v", conflict.Options)
	}
	if conflict.Options[0].Detail == nil || !strings.Contains(*conflict.Options[0].Detail, "keep_current") ||
		conflict.Options[1].Detail == nil || !strings.Contains(*conflict.Options[1].Detail, "accept_proposal") {
		t.Fatalf("conflict option provenance = %+v", conflict.Options)
	}
}

func TestClarifyQuestionsSpeakTheRequestedLocale(t *testing.T) {
	current := "Acme"
	comparisons := []people.SiteReadComparison{{Key: "display_name", Classification: "human_conflict", CurrentValue: &current, ProposedValue: "Acme GmbH"}}
	read := people.SiteRead{LegalEntities: []people.SiteReadLegalEntity{
		{Name: "Acme GmbH", SourceURL: "https://a.example"}, {Name: "Acme AG", SourceURL: "https://a.example"},
	}}
	en := onboardingClarifies(read, comparisons, "en")
	de := onboardingClarifies(read, comparisons, "de")
	if len(en) != 2 || len(de) != 2 {
		t.Fatalf("clarify counts en=%d de=%d", len(en), len(de))
	}
	for i := range en {
		if en[i].Question == de[i].Question {
			t.Fatalf("question %d is not localized: %q", i, en[i].Question)
		}
		if en[i].Options[0].Value != de[i].Options[0].Value {
			t.Fatalf("option values must not vary by locale: %+v vs %+v", en[i].Options[0], de[i].Options[0])
		}
	}
}
