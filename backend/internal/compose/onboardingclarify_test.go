// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
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

// The live-data failure this pins: a census row whose "name" is the page's
// navigation chrome and whose "address" is a mis-paired link trail must not
// mint unanswerable options that block the save.
func TestEntityClarifiesFilterImplausibleCensusDebris(t *testing.T) {
	chromeName := "Gradion Pte. Ltd.Solutions Products Industries About Careers Contact English Deutsch Solutions Products Industries About Careers Contact English Deutsch"
	chromeAddress := "Solutions Products Industries About Careers Contact English Deutsch"
	clean := people.SiteReadLegalEntity{Name: "Gradion Pte. Ltd.", RegisteredAddress: "10 Anson Road #22-02, Singapore 079903", SourceURL: "https://gradion.example/legal"}

	t.Run("one plausible survivor means no question at all", func(t *testing.T) {
		read := people.SiteRead{DraftVersion: 1, LegalEntities: []people.SiteReadLegalEntity{
			clean,
			{Name: chromeName, RegisteredAddress: chromeAddress, SourceURL: "https://gradion.example/legal"},
		}}
		if got := onboardingClarifies(read, nil, "en"); len(got) != 0 {
			t.Fatalf("debris minted a question: %+v", got)
		}
	})

	t.Run("two plausible survivors ask with only the plausible options", func(t *testing.T) {
		read := people.SiteRead{DraftVersion: 1, LegalEntities: []people.SiteReadLegalEntity{
			clean,
			{Name: "Gradion GmbH", RegisteredAddress: "Musterstraße 1, 10115 Berlin", SourceURL: "https://gradion.example/de/legal"},
			{Name: chromeName, RegisteredAddress: chromeAddress, SourceURL: "https://gradion.example/legal"},
		}}
		clarifies := onboardingClarifies(read, nil, "en")
		if len(clarifies) != 2 {
			t.Fatalf("clarifies = %+v", clarifies)
		}
		for _, clarify := range clarifies {
			if len(clarify.Options) != 2 {
				t.Fatalf("%s options = %+v", clarify.Field, clarify.Options)
			}
			for _, option := range clarify.Options {
				if strings.Contains(option.Value, "Solutions Products") {
					t.Fatalf("chrome survived the filter: %+v", option)
				}
			}
		}
	})

	t.Run("the verify gate accepts exactly the surviving set", func(t *testing.T) {
		read := &people.SiteRead{DraftVersion: 1, LegalEntities: []people.SiteReadLegalEntity{
			clean,
			{Name: "Gradion GmbH", RegisteredAddress: "Musterstraße 1, 10115 Berlin", SourceURL: "https://gradion.example/de/legal"},
			{Name: chromeName, RegisteredAddress: chromeAddress, SourceURL: "https://gradion.example/legal"},
		}}
		surviving := crmcontracts.OnboardingClarifySelection{ClarifyId: "clarify:legal_name:1", Field: "legal_name", Value: "Gradion GmbH"}
		if err := verifySelectedOption(surviving, read, nil, "en"); err != nil {
			t.Fatalf("surviving option refused: %v", err)
		}
		filtered := crmcontracts.OnboardingClarifySelection{ClarifyId: "clarify:legal_name:1", Field: "legal_name", Value: chromeName}
		if err := verifySelectedOption(filtered, read, nil, "en"); err == nil {
			t.Fatal("a filtered-out debris value was accepted as a selection")
		}
	})
}

func TestPlausibleClarifyValueBounds(t *testing.T) {
	tests := map[string]struct {
		raw          string
		maxRunes     int
		addressShape bool
		want         string
		wantOK       bool
	}{
		"clean name passes and collapses whitespace": {raw: "  Acme   GmbH ", maxRunes: clarifyNameMaxRunes, want: "Acme GmbH", wantOK: true},
		"multi-line scrape rejected":                 {raw: "Acme GmbH\nMusterstraße 1", maxRunes: clarifyNameMaxRunes},
		"over-cap name rejected":                     {raw: strings.Repeat("Gradion Solutions ", 8), maxRunes: clarifyNameMaxRunes},
		"token repeated three times rejected":        {raw: "Home Home Home GmbH", maxRunes: clarifyNameMaxRunes},
		"long plain word trail is no address":        {raw: "Solutions Products Industries About Careers Contact English", maxRunes: clarifyAddressMaxRunes, addressShape: true},
		"real address with digits passes":            {raw: "10 Anson Road #22-02, Singapore 079903", maxRunes: clarifyAddressMaxRunes, addressShape: true, want: "10 Anson Road #22-02, Singapore 079903", wantOK: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := plausibleClarifyValue(tc.raw, tc.maxRunes, tc.addressShape)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("plausibleClarifyValue(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
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

func TestOpenClarifiesSkipQuestionsTheDraftAlreadyAnswers(t *testing.T) {
	current := "Acme Software"
	read := people.SiteRead{DraftVersion: 5, LegalEntities: []people.SiteReadLegalEntity{
		{Name: "Acme GmbH", RegisteredAddress: "Berlin 1", SourceURL: "https://acme.example/legal"},
		{Name: "Acme Holding AG", RegisteredAddress: "Zug 2", SourceURL: "https://acme.example/legal"},
	}}
	comparisons := []people.SiteReadComparison{{Key: "display_name", Classification: "human_conflict", CurrentValue: &current, ProposedValue: "Acme GmbH"}}
	openFields := func(draft identity.OnboardingCompanyDraft) []string {
		open := openOnboardingClarifies(read, comparisons, "en", draft)
		fields := make([]string, 0, len(open))
		for _, clarify := range open {
			fields = append(fields, clarify.Field)
		}
		return fields
	}

	tests := map[string]struct {
		draft identity.OnboardingCompanyDraft
		want  []string
	}{
		"empty draft keeps every question open": {
			want: []string{fieldLegalName, fieldRegisteredAddress, fieldDisplayName},
		},
		"an answered entity question is not re-asked": {
			draft: identity.OnboardingCompanyDraft{LegalName: stringPtr("Acme GmbH")},
			want:  []string{fieldRegisteredAddress, fieldDisplayName},
		},
		"a selection echo with surrounding whitespace still resolves": {
			draft: identity.OnboardingCompanyDraft{RegisteredAddress: stringPtr("  Berlin 1  ")},
			want:  []string{fieldLegalName, fieldDisplayName},
		},
		"a resolved conflict is not re-asked (either option value)": {
			draft: identity.OnboardingCompanyDraft{DisplayName: stringPtr("Acme Software")},
			want:  []string{fieldLegalName, fieldRegisteredAddress},
		},
		// The documented boundary: only an exact option-value match is
		// provably an answer. A hand-typed different value could equally
		// be a read prefill, so the question stays open.
		"a hand-typed non-option value keeps the question open": {
			draft: identity.OnboardingCompanyDraft{LegalName: stringPtr("Acme Worldwide Ltd")},
			want:  []string{fieldLegalName, fieldRegisteredAddress, fieldDisplayName},
		},
		"a blank draft value keeps the question open": {
			draft: identity.OnboardingCompanyDraft{LegalName: stringPtr("   ")},
			want:  []string{fieldLegalName, fieldRegisteredAddress, fieldDisplayName},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := openFields(tc.draft)
			if len(got) != len(tc.want) {
				t.Fatalf("open fields = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("open fields = %v, want %v", got, tc.want)
				}
			}
		})
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
