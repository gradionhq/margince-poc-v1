// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// The legal census only reaches a human through this projection: the
// confirm step's entity choice IS this array. A detail the page never
// printed must arrive ABSENT rather than as an empty string — "the notice
// states no register number for this entity" and "this entity has a blank
// register number" are different claims, and only one of them is true.
func TestCompanySiteReadCarriesTheLegalCensus(t *testing.T) {
	read := people.SiteRead{
		SeedURL: seedURL,
		Status:  "partial",
		LegalEntities: []people.SiteReadLegalEntity{
			{
				Name:              "Acme GmbH",
				RegisteredAddress: "Deliusstrasse 7, 24114 Kiel",
				RegisterNumber:    "HRB 12345",
				EvidenceSnippet:   "Acme GmbH, Deliusstrasse 7, 24114 Kiel. HRB 12345.",
				SourceURL:         seedURL + "/imprint",
			},
			{Name: "Acme Pte. Ltd.", SourceURL: seedURL + "/imprint"},
		},
	}

	got := companySiteRead(read, nil)
	if got.LegalEntities == nil {
		t.Fatal("the census never reached the wire")
	}
	entities := *got.LegalEntities
	if len(entities) != 2 {
		t.Fatalf("both entities must reach the wire: %+v", entities)
	}
	if entities[0].RegisteredAddress == nil || *entities[0].RegisteredAddress != "Deliusstrasse 7, 24114 Kiel" {
		t.Errorf("the printed address must survive the projection: %+v", entities[0])
	}
	if entities[0].RegisterNumber == nil || *entities[0].RegisterNumber != "HRB 12345" {
		t.Errorf("the printed register number must survive the projection: %+v", entities[0])
	}
	if entities[1].RegisteredAddress != nil || entities[1].RegisterNumber != nil {
		t.Errorf("a detail the page never printed must be absent, not empty: %+v", entities[1])
	}
	if entities[1].Name != "Acme Pte. Ltd." {
		t.Errorf("the entity name is the one field a census entry always has: %+v", entities[1])
	}
}

// A site with no legal notice states no entities: the array is empty, and
// the client renders no choice rather than an empty question.
func TestCompanySiteReadCensusIsEmptyWhenNothingWasRead(t *testing.T) {
	got := companySiteRead(people.SiteRead{SeedURL: seedURL, Status: "done"}, nil)
	if got.LegalEntities == nil {
		t.Fatal("the field must be present and empty, never null")
	}
	if len(*got.LegalEntities) != 0 {
		t.Fatalf("no legal page read means no entities: %+v", *got.LegalEntities)
	}
}
