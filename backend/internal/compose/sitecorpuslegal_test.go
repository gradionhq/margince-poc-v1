// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

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
