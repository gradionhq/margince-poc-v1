// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

	got := companySiteRead(read, nil, nil)
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
	got := companySiteRead(people.SiteRead{SeedURL: seedURL, Status: "done"}, nil, nil)
	if got.LegalEntities == nil {
		t.Fatal("the field must be present and empty, never null")
	}
	if len(*got.LegalEntities) != 0 {
		t.Fatalf("no legal page read means no entities: %+v", *got.LegalEntities)
	}
}

// A crawl that ran into a cap, the deadline or the budget ended by decision,
// not by fault — and the cold start is the surface with the least context to
// tell those apart. Without the reason on the wire, a thin-but-honest read
// and a broken one are the same screen.
func TestCompanySiteReadSaysWhyTheCrawlStopped(t *testing.T) {
	stopped := "page_cap"
	got := companySiteRead(people.SiteRead{
		SeedURL: seedURL, Status: "partial", StoppedReason: &stopped,
	}, nil, nil)
	if got.StoppedReason == nil {
		t.Fatal("a bounded read must be able to say what bounded it")
	}
	if *got.StoppedReason != crmcontracts.CompanySiteReadStoppedReasonPageCap {
		t.Errorf("stopped_reason = %q, want the page cap the store recorded", *got.StoppedReason)
	}

	// Discovery ran out on its own: nothing stopped this read, so the wire
	// says nothing rather than naming a cause that never fired.
	exhausted := companySiteRead(people.SiteRead{SeedURL: seedURL, Status: "done"}, nil, nil)
	if exhausted.StoppedReason != nil {
		t.Errorf("a read that exhausted discovery stopped for no reason: %q", *exhausted.StoppedReason)
	}
}
