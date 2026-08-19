// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// A group's legal notice states the same entity more than once: every
// locale of the page repeats it, and each block is headed by the market
// it trades in. The census a human is offered must be the companies, not
// the sightings.
func TestDedupeLegalEntitiesFoldsOnTheRegisterNumber(t *testing.T) {
	entities := []corpusLegalEntity{
		{Name: "Acme Pte. Ltd.", RegisterNumber: "201629357M", SourceURL: seedURL + "/imprint"},
		// The market heading printed above the block, which the page
		// states as prominently as the entity itself.
		{Name: "Acme Singapore", RegisterNumber: "201629357M", SourceURL: seedURL + "/imprint"},
		// The German locale of the same page, this time with the address.
		{
			Name: "Acme Pte. Ltd.", RegisteredAddress: "77 High Street, Singapore",
			RegisterNumber: "201629357M", SourceURL: seedURL + "/de/imprint",
		},
		// Another locale can lose the register number at a passage boundary.
		// Its matching name must not create a duplicate choice for the human.
		{Name: "Acme Pte. Ltd.", SourceURL: seedURL + "/th/imprint"},
		{Name: "Acme GmbH", RegisterNumber: "HRB 12345", SourceURL: seedURL + "/imprint"},
	}
	got := dedupeLegalEntities(entities)
	if len(got) != 2 {
		t.Fatalf("four sightings of two companies must fold to two: %+v", got)
	}
	if got[0].RegisteredAddress != "77 High Street, Singapore" {
		t.Errorf("the richest sighting must win, so a locale that printed the address is not lost: %+v", got[0])
	}
	if got[1].Name != "Acme GmbH" {
		t.Errorf("a distinct register number is a distinct company: %+v", got[1])
	}
}

func TestDedupeLegalEntitiesKeepsSameNameWithDistinctRegisters(t *testing.T) {
	entities := []corpusLegalEntity{
		{Name: "Acme Ltd", RegisterNumber: "SG-1", SourceURL: seedURL + "/imprint"},
		{Name: "Acme Ltd", RegisterNumber: "UK-2", SourceURL: seedURL + "/en/imprint"},
	}
	if got := dedupeLegalEntities(entities); len(got) != 2 {
		t.Fatalf("different registry identities must remain separate: %+v", got)
	}
}

func TestDedupeLegalEntitiesFoldsPunctuationVariantsAndDropsBareBrand(t *testing.T) {
	entities := []corpusLegalEntity{
		{Name: "RealtimeBoard, Inc. dba Miro", RegisteredAddress: "San Francisco", SourceURL: seedURL + "/legal"},
		{Name: "RealtimeBoard Inc dba Miro", SourceURL: seedURL + "/imprint"},
		{Name: "RealtimeBoard B.V.", RegisterNumber: "123", SourceURL: seedURL + "/legal"},
		{Name: "RealtimeBoard BV", RegisterNumber: "123", SourceURL: seedURL + "/imprint"},
		{Name: "Miro", SourceURL: seedURL + "/legal"},
	}
	got := dedupeLegalEntities(entities)
	if len(got) != 2 {
		t.Fatalf("punctuation variants and a bare brand must not become legal choices: %+v", got)
	}
	if got[0].RegisteredAddress != "San Francisco" || got[1].RegisterNumber != "123" {
		t.Fatalf("the richest registered sightings must survive: %+v", got)
	}
}

func TestDedupeLegalEntitiesKeepsTheOnlyBareLegalName(t *testing.T) {
	got := dedupeLegalEntities([]corpusLegalEntity{{Name: "Miro", SourceURL: seedURL + "/legal"}})
	if len(got) != 1 {
		t.Fatalf("an unusual legal name must survive when no richer registered alias exists: %+v", got)
	}
}

func TestEnrichSingleLegalEntityFromGatedProfile(t *testing.T) {
	entities := []corpusLegalEntity{{Name: "Acme GmbH", SourceURL: seedURL + "/imprint"}}
	fields := []evidencedField{
		{Field: "registered_address", Value: "Deliusstrasse 7, 24114 Kiel"},
		{Field: "register_vat", Value: "HRB 123456"},
	}
	got := enrichLegalEntitiesFromProfile(entities, fields)
	if got[0].RegisteredAddress == "" || got[0].RegisterNumber != "HRB 123456" {
		t.Fatalf("the single legal choice must reuse the already-gated trio: %+v", got)
	}
	if entities[0].RegisteredAddress != "" {
		t.Fatal("enrichment must not mutate the source slice")
	}
	if many := enrichLegalEntitiesFromProfile(append(entities, corpusLegalEntity{Name: "Acme Inc."}), fields); many[0].RegisterNumber != "" {
		t.Fatal("profile values must never be assigned across a multi-entity census")
	}
}

// Without a register number there is nothing authoritative to fold on, so
// the name is the identity — two genuinely different names stay two.
func TestDedupeLegalEntitiesFallsBackToTheNameWithoutARegisterNumber(t *testing.T) {
	entities := []corpusLegalEntity{
		{Name: "Acme GmbH", SourceURL: seedURL + "/imprint"},
		{Name: "Acme GmbH", RegisteredAddress: "Kiel", SourceURL: seedURL + "/de/imprint"},
		{Name: "Acme Ltd", SourceURL: seedURL + "/imprint"},
	}
	got := dedupeLegalEntities(entities)
	if len(got) != 2 {
		t.Fatalf("two names must survive as two entities: %+v", got)
	}
	if got[0].RegisteredAddress != "Kiel" {
		t.Errorf("the sighting that carried the address must win: %+v", got[0])
	}
}

func TestLegalEntityDetailCountsWhatWasPrinted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		entity corpusLegalEntity
		want   int
	}{
		{"name only", corpusLegalEntity{Name: "Acme GmbH"}, 0},
		{"with address", corpusLegalEntity{Name: "Acme GmbH", RegisteredAddress: "Kiel"}, 1},
		{"the whole block", corpusLegalEntity{Name: "Acme GmbH", RegisteredAddress: "Kiel", RegisterNumber: "HRB 1"}, 2},
		{"blank is not printed", corpusLegalEntity{Name: "Acme GmbH", RegisteredAddress: "  "}, 0},
	} {
		if got := legalEntityDetail(tc.entity); got != tc.want {
			t.Errorf("%s: legalEntityDetail = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The trio is withheld for two unrelated reasons, and the human is told
// which. A run whose legal page failed to extract must never be told the
// domain hosts several companies — that is a corporate structure nobody
// read, stated as fact on the strength of a provider outage.
func TestLegalAbstentionNamesTheCauseThatFired(t *testing.T) {
	one := []corpusLegalEntity{{Name: "Acme GmbH", SourceURL: seedURL + "/impressum"}}
	two := []corpusLegalEntity{
		{Name: "Acme GmbH", SourceURL: seedURL + "/impressum"},
		{Name: "Acme Pte. Ltd.", SourceURL: seedURL + "/impressum"},
	}
	for _, tc := range []struct {
		name             string
		entities         []corpusLegalEntity
		censusIncomplete bool
		want             legalAbstention
	}{
		{"a legal page that never came back", one, true, legalAbstentionCensusIncomplete},
		{"a domain that states two entities", two, false, legalAbstentionMultipleEntities},
		{"both: what the site publishes outranks what this run missed", two, true, legalAbstentionMultipleEntities},
		{"a settled census", one, false, legalAbstentionNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := legalAbstentionOf(tc.entities, tc.censusIncomplete)
			if got != tc.want {
				t.Fatalf("abstention = %q, want %q", got, tc.want)
			}
			// The drop record and the sentence a human reads answer the
			// same question, so they are derived from the same value.
			if want := tc.want.warning(); got.warning() != want {
				t.Errorf("warning = %q, want %q", got.warning(), want)
			}
		})
	}
	if legalAbstentionCensusIncomplete.warning() == legalAbstentionMultipleEntities.warning() {
		t.Fatal("the two causes must not share one sentence")
	}
	if strings.Contains(legalWarningCensusIncomplete, "more than one entity") {
		t.Errorf("the incomplete-census warning must claim nothing about the number of entities: %q", legalWarningCensusIncomplete)
	}
}

// TestCensusFillsTheAddressTheProfileLaneMissed is the fix for the gap that
// left 136 of the demo dataset's 190 companies with no address at all.
//
// The two legal lanes read the same page and disagree. The census reads the
// whole page; the profile lane reads a bounded excerpt and gates against the
// single passage the model cited. communicode.de's census carried
// "Wittekindstr. 1a, 45131 Essen" while its profile lane dropped the same
// address for citing a neighbouring passage, so the field never existed.
func TestCensusFillsTheAddressTheProfileLaneMissed(t *testing.T) {
	const impressum = "https://example.com/impressum"
	kinds := map[string]crmcontracts.SiteReadPageKind{
		impressum: crmcontracts.SiteReadPageKindImpressum,
	}
	entities := []corpusLegalEntity{{
		Name:              "communicode GmbH",
		RegisteredAddress: "Wittekindstr. 1a, 45131 Essen",
		RegisterNumber:    "HRB 37643",
		EvidenceSnippet:   "Impressum communicode GmbH, Wittekindstr. 1a in 45131 Essen",
		SourceURL:         impressum,
	}}
	// The profile lane produced only the display name: its legal trio was
	// dropped, which is the real state this replays.
	fields := []evidencedField{{Field: "display_name", Value: "communicode", SourceURL: impressum, Confidence: 1}}

	out := fillLegalTrioFromCensus(fields, entities, kinds, false)
	got := map[string]string{}
	for _, f := range out {
		got[f.Field] = f.Value
	}
	if got["registered_address"] != "Wittekindstr. 1a, 45131 Essen" {
		t.Errorf("registered_address = %q, want the address the census proved", got["registered_address"])
	}
	if got["legal_name"] != "communicode GmbH" {
		t.Errorf("legal_name = %q", got["legal_name"])
	}
	if got["register_vat"] != "HRB 37643" {
		t.Errorf("register_vat = %q", got["register_vat"])
	}
	// Every filled field must carry the evidence and the page, so the legal
	// gate can judge it exactly as it judges a profile-lane field.
	for _, f := range out {
		if f.Field == "display_name" {
			continue
		}
		if f.EvidenceSnippet == "" || f.SourceURL != impressum {
			t.Errorf("%s arrived without evidence or a source page", f.Field)
		}
	}
}

// TestCensusFillNeverOverwritesTheProfileLane — a field the profile lane
// produced and the gate kept is the more specific answer and stands.
func TestCensusFillNeverOverwritesTheProfileLane(t *testing.T) {
	const impressum = "https://example.com/impressum"
	kinds := map[string]crmcontracts.SiteReadPageKind{impressum: crmcontracts.SiteReadPageKindImpressum}
	entities := []corpusLegalEntity{{
		Name: "Census GmbH", RegisteredAddress: "Census Weg 1 12345 Ort",
		EvidenceSnippet: "Census GmbH Census Weg 1 12345 Ort", SourceURL: impressum,
	}}
	fields := []evidencedField{{
		Field: "registered_address", Value: "Profile Strasse 2 54321 Stadt",
		EvidenceSnippet: "Profile Strasse 2 54321 Stadt", SourceURL: impressum, Confidence: 0.9,
	}}
	out := fillLegalTrioFromCensus(fields, entities, kinds, false)
	count := 0
	for _, f := range out {
		if f.Field != "registered_address" {
			continue
		}
		count++
		if f.Value != "Profile Strasse 2 54321 Stadt" {
			t.Errorf("the profile lane's value was replaced by %q", f.Value)
		}
	}
	if count != 1 {
		t.Errorf("registered_address appears %d times, want exactly one", count)
	}
}

// TestCensusFillRespectsTheAbstentionAndTheAuthorityRule pins the two ways
// this must refuse. Filling from a census the gate just judged untrustworthy
// would put back exactly what the abstention withheld, and a sighting from a
// page that is not a legal notice must not speak for legal identity.
func TestCensusFillRespectsTheAbstentionAndTheAuthorityRule(t *testing.T) {
	const impressum = "https://example.com/impressum"
	entity := corpusLegalEntity{
		Name: "Anything GmbH", RegisteredAddress: "Irgendwo 1 11111 Ort",
		EvidenceSnippet: "Anything GmbH Irgendwo 1 11111 Ort", SourceURL: impressum,
	}
	legalKinds := map[string]crmcontracts.SiteReadPageKind{impressum: crmcontracts.SiteReadPageKindImpressum}

	if got := fillLegalTrioFromCensus(nil, []corpusLegalEntity{entity}, legalKinds, true); len(got) != 0 {
		t.Error("an abstained read was filled from the census it abstained on")
	}
	two := []corpusLegalEntity{entity, {Name: "Other GmbH", SourceURL: impressum}}
	if got := fillLegalTrioFromCensus(nil, two, legalKinds, false); len(got) != 0 {
		t.Error("an unsettled census with two entities was used to fill legal identity")
	}
	// A contact page is not legal authority, however plainly it prints an address.
	contactOnly := map[string]crmcontracts.SiteReadPageKind{
		impressum: crmcontracts.SiteReadPageKindContact,
	}
	if got := fillLegalTrioFromCensus(nil, []corpusLegalEntity{entity}, contactOnly, false); len(got) != 0 {
		t.Error("a non-legal page was allowed to state the company's legal identity")
	}
	// And a deep path is content ABOUT a legal page, not one.
	deep := entity
	deep.SourceURL = "https://example.com/a/b/c/impressum"
	deepKinds := map[string]crmcontracts.SiteReadPageKind{deep.SourceURL: crmcontracts.SiteReadPageKindImpressum}
	if got := fillLegalTrioFromCensus(nil, []corpusLegalEntity{deep}, deepKinds, false); len(got) != 0 {
		t.Error("a deep-path legal page was treated as the company's own")
	}
}

// TestCensusFillAddsNothingWhenTheCensusHasNothing — an entity stating a name
// and no details fills only the name; the absent details stay absent rather
// than becoming empty fields.
func TestCensusFillAddsNothingWhenTheCensusHasNothing(t *testing.T) {
	const impressum = "https://example.com/impressum"
	kinds := map[string]crmcontracts.SiteReadPageKind{impressum: crmcontracts.SiteReadPageKindImpressum}
	entities := []corpusLegalEntity{{Name: "Bare GmbH", SourceURL: impressum, EvidenceSnippet: "Bare GmbH"}}
	out := fillLegalTrioFromCensus(nil, entities, kinds, false)
	if len(out) != 1 || out[0].Field != "legal_name" {
		t.Fatalf("want just the legal name, got %v", out)
	}
}

// TestCensusFillIsDeterministic pins the field order.
//
// These fields reach the proposal whose JSON is hashed, so iterating a Go map
// — which the first version of this did — gave identical evidence a different
// hash on each run.
func TestCensusFillIsDeterministic(t *testing.T) {
	const impressum = "https://example.com/impressum"
	kinds := map[string]crmcontracts.SiteReadPageKind{impressum: crmcontracts.SiteReadPageKindImpressum}
	entities := []corpusLegalEntity{{
		Name: "Order GmbH", RegisteredAddress: "Weg 1 11111 Ort", RegisterNumber: "HRB 999",
		EvidenceSnippet: "Order GmbH Weg 1 11111 Ort HRB 999", SourceURL: impressum,
	}}
	want := []string{"legal_name", "registered_address", "register_vat"}
	for range 20 {
		out := fillLegalTrioFromCensus(nil, entities, kinds, false)
		if len(out) != len(want) {
			t.Fatalf("got %d fields, want %d", len(out), len(want))
		}
		for i, field := range want {
			if out[i].Field != field {
				t.Fatalf("field %d is %q, want %q — the order is not stable", i, out[i].Field, field)
			}
		}
	}
}
