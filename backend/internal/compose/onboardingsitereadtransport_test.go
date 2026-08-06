// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// Three different things went wrong and three different things put them right,
// but all three answer 409 — so the code is the only thing the client can steer
// on. One code shared between two of them sends it back to the server to work
// out which it was.
func TestConfirmingASiteReadGivesEachRefusalItsOwnCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "already confirmed",
			err:  fmt.Errorf("confirm company site read: %w", people.ErrSiteReadAlreadyConfirmed),
			code: "already_confirmed",
		},
		{
			name: "no draft to confirm yet",
			err:  fmt.Errorf("confirm company site read: %w", people.ErrSiteReadNotConfirmable),
			code: "not_confirmable",
		},
		{
			name: "draft moved since it was reviewed",
			err:  fmt.Errorf("the website draft changed since it was reviewed: %w", apperrors.ErrVersionSkew),
			code: "version_skew",
		},
	}
	answered := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/company/site-reads/x/confirm", nil)
			httperr.Write(recorder, request, siteReadConfirmationRefusal(tc.err))

			var problem struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusConflict || problem.Code != tc.code {
				t.Fatalf("refusal → %d %q, want 409 %q", recorder.Code, problem.Code, tc.code)
			}
			if problem.Detail == "" {
				t.Fatal("the refusal says nothing about what happened or what to do")
			}
			if previous, taken := answered[problem.Code]; taken {
				t.Fatalf("%q answers both %q and %q", problem.Code, previous, tc.name)
			}
			answered[problem.Code] = tc.name
		})
	}
}

// The refusal switch names two errors; it is not a place every other error goes
// to lose the mapping it already had.
func TestConfirmingASiteReadLeavesUnrelatedErrorsAlone(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/company/site-reads/x/confirm", nil)
	httperr.Write(recorder, request, siteReadConfirmationRefusal(apperrors.ErrNotFound))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a dossier that does not exist → %d, want 404", recorder.Code)
	}
}
