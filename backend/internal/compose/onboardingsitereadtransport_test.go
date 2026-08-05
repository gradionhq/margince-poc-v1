// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

func TestOnboardingSiteReadHandlersStayExplicitWithoutAConfiguredEngine(t *testing.T) {
	handlers := siteReadHandlers{}
	readID := openapi_types.UUID(ids.NewV7())
	tests := []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) {
			handlers.StartCompanySiteRead(w, r, crmcontracts.StartCompanySiteReadParams{})
		},
		func(w http.ResponseWriter, r *http.Request) { handlers.GetCompanySiteRead(w, r, readID) },
		func(w http.ResponseWriter, r *http.Request) {
			handlers.ConfirmCompanySiteRead(w, r, readID, crmcontracts.ConfirmCompanySiteReadParams{})
		},
	}
	for i, invoke := range tests {
		rec := httptest.NewRecorder()
		invoke(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("unconfigured handler %d → %d, want 501", i, rec.Code)
		}
	}
}
