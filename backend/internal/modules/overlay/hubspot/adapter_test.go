// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// searchModifiedContactsJSON is a design §11-shaped Search response for
// two contacts, ascending by lastmodifieddate.
const searchModifiedContactsJSON = `{
  "total": 2,
  "results": [
    { "id": "100214862042",
      "properties": { "hs_object_id": "100214862042", "firstname": "Christian", "lastname": "Mueller",
        "hubspot_owner_id": "1197833249", "lastmodifieddate": "2026-05-13T06:44:38.727Z" } },
    { "id": "100214862099",
      "properties": { "hs_object_id": "100214862099", "firstname": "Anna", "lastname": "Schmidt",
        "hubspot_owner_id": "1197833250", "lastmodifieddate": "2026-05-14T09:12:01.100Z" } }
  ],
  "paging": { "next": { "after": "2" } }
}`

// TestAdapterModifiedUsesLastModifiedDateWatermarkForContacts drives the
// real Client (against an httptest.Server) through the Adapter's
// Modified method: it must sort/filter by contacts' lastmodifieddate
// watermark property (design §7) and map the raw search results into
// ascending overlay.Records carrying ModifiedAt and OwnerExternalID.
func TestAdapterModifiedUsesLastModifiedDateWatermarkForContacts(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/objects/contacts/search" {
			t.Fatalf("path = %q, want /crm/v3/objects/contacts/search", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchModifiedContactsJSON))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	page, err := adapter.Modified(t.Context(), "contacts", since, "")
	if err != nil {
		t.Fatalf("Modified: unexpected error: %v", err)
	}

	sorts, _ := gotBody["sorts"].([]any)
	if len(sorts) != 1 {
		t.Fatalf("sorts = %#v, want exactly one sort", gotBody["sorts"])
	}
	sort0, _ := sorts[0].(map[string]any)
	if sort0["propertyName"] != "lastmodifieddate" {
		t.Fatalf("sorts[0].propertyName = %v, want lastmodifieddate (design §7's contacts watermark)", sort0["propertyName"])
	}

	if page.NextCursor != "2" {
		t.Fatalf("NextCursor = %q, want 2", page.NextCursor)
	}
	if len(page.Records) != 2 {
		t.Fatalf("len(Records) = %d, want 2", len(page.Records))
	}

	first, second := page.Records[0], page.Records[1]
	if first.ExternalID != "100214862042" || second.ExternalID != "100214862099" {
		t.Fatalf("Records = %#v, want ascending 100214862042 then 100214862099", page.Records)
	}
	if !first.ModifiedAt.Before(second.ModifiedAt) {
		t.Fatalf("ModifiedAt not ascending: first=%v second=%v", first.ModifiedAt, second.ModifiedAt)
	}
	wantFirstModified := time.Date(2026, 5, 13, 6, 44, 38, 727000000, time.UTC)
	if !first.ModifiedAt.Equal(wantFirstModified) {
		t.Fatalf("first.ModifiedAt = %v, want %v", first.ModifiedAt, wantFirstModified)
	}
	if first.OwnerExternalID != "1197833249" {
		t.Fatalf("first.OwnerExternalID = %q, want 1197833249", first.OwnerExternalID)
	}
	// ObjectClass is the canonical entity type (mapping.Target), not the
	// incumbent source name — the datasource read seam reads mirror rows
	// by canonical EntityType, so "contacts" must never leak here.
	if first.ObjectClass != "person" {
		t.Fatalf("first.ObjectClass = %q, want person (the canonical entity type, not the incumbent source name)", first.ObjectClass)
	}
	if got := first.Fields["first_name"]; got != "Christian" {
		t.Fatalf("first.Fields[first_name] = %v, want Christian", got)
	}
}

// TestAdapterBackfillPagesViaListCursor exercises Backfill's delegation
// to the Client's id-keyset List endpoint (the uncapped backfill cursor,
// design §11), distinct from Modified's Search-based watermark sweep.
func TestAdapterBackfillPagesViaListCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/objects/contacts" {
			t.Fatalf("path = %q, want /crm/v3/objects/contacts", r.URL.Path)
		}
		if got := r.URL.Query().Get("after"); got != "50" {
			t.Fatalf("after = %q, want 50 (the cursor Backfill was called with)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [ { "id": "1", "properties": { "hs_object_id": "1", "firstname": "A", "lastname": "B",
			  "lastmodifieddate": "2026-01-01T00:00:00.000Z" } } ],
			"paging": { "next": { "after": "51" } }
		}`))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	page, err := adapter.Backfill(t.Context(), "contacts", "50")
	if err != nil {
		t.Fatalf("Backfill: unexpected error: %v", err)
	}
	if page.NextCursor != "51" {
		t.Fatalf("NextCursor = %q, want 51", page.NextCursor)
	}
	if len(page.Records) != 1 || page.Records[0].ExternalID != "1" {
		t.Fatalf("Records = %#v, want one record with external id 1", page.Records)
	}
}

// TestAdapterGetFetchesViaBatchRead exercises Get's delegation to
// BatchRead — the record-clock fetch a force-fresh read-through uses.
func TestAdapterGetFetchesViaBatchRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/objects/contacts/batch/read" {
			t.Fatalf("path = %q, want /crm/v3/objects/contacts/batch/read", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"100214862042","properties":{
			"hs_object_id":"100214862042","firstname":"Christian","lastname":"Mueller",
			"lastmodifieddate":"2026-05-13T06:44:38.727Z"}}]}`))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	rec, err := adapter.Get(t.Context(), "contacts", "100214862042")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if rec.ExternalID != "100214862042" {
		t.Fatalf("ExternalID = %q, want 100214862042", rec.ExternalID)
	}
	if rec.Fields["first_name"] != "Christian" {
		t.Fatalf("Fields[first_name] = %v, want Christian", rec.Fields["first_name"])
	}
}

// TestAdapterGetNoResultsErrorsCleanly exercises the not-found path: an
// empty BatchRead result is a clean error, never a nil-slice panic.
func TestAdapterGetNoResultsErrorsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	if _, err := adapter.Get(t.Context(), "contacts", "does-not-exist"); err == nil {
		t.Fatalf("Get: expected an error for a missing record, got nil")
	}
}

// TestAdapterAssociationsPopulatesForwardDirection exercises
// Associations' delegation to the v4 endpoint, one overlay.Assoc per
// association-type label, each tagged with the forward direction this
// query resolves.
func TestAdapterAssociationsPopulatesForwardDirection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v4/objects/deals/123/associations/companies" {
			t.Fatalf("path = %q, want /crm/v4/objects/deals/123/associations/companies", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [ { "toObjectId": 61655665850,
			  "associationTypes": [ {"category":"HUBSPOT_DEFINED","typeId":5,"label":"Primary"} ] } ]
		}`))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	assocs, err := adapter.Associations(t.Context(), "deals", "123", "companies")
	if err != nil {
		t.Fatalf("Associations: unexpected error: %v", err)
	}
	if len(assocs) != 1 {
		t.Fatalf("len(assocs) = %d, want 1", len(assocs))
	}
	got := assocs[0]
	want := overlay.Assoc{
		FromType: "deals", FromID: "123", ToType: "companies", ToID: "61655665850",
		TypeID: 5, Category: "HUBSPOT_DEFINED", Label: "Primary", Direction: "forward",
	}
	if got != want {
		t.Fatalf("assocs[0] = %#v, want %#v", got, want)
	}
}

// TestAdapterOwnerEmailResolvesViaOwnersAPI exercises OwnerEmail's
// delegation to the Owners API (design §4.3's mirror_user_map
// resolution).
func TestAdapterOwnerEmailResolvesViaOwnersAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/owners/1197833249" {
			t.Fatalf("path = %q, want /crm/v3/owners/1197833249", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1197833249","email":"christian@example.de"}`))
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	email, err := adapter.OwnerEmail(t.Context(), "1197833249")
	if err != nil {
		t.Fatalf("OwnerEmail: unexpected error: %v", err)
	}
	if email != "christian@example.de" {
		t.Fatalf("OwnerEmail = %q, want christian@example.de", email)
	}
}

// TestAdapterOwnersPagesTheDirectory proves Owners follows the CRM
// Owners API's paging.next.after cursor to completion — the full owner
// set mirror_user_map seeding matches against, not just the first page.
func TestAdapterOwnersPagesTheDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/owners" {
			t.Fatalf("path = %q, want /crm/v3/owners", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after") {
		case "":
			_, _ = w.Write([]byte(`{"results":[{"id":"1","email":"alice@example.com"}],"paging":{"next":{"after":"p2"}}}`))
		case "p2":
			_, _ = w.Write([]byte(`{"results":[{"id":"2","email":"bob@example.com"}]}`))
		default:
			t.Fatalf("unexpected after cursor %q", r.URL.Query().Get("after"))
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL)))

	owners, err := adapter.Owners(t.Context())
	if err != nil {
		t.Fatalf("Owners: %v", err)
	}
	got := map[string]string{}
	for _, o := range owners {
		got[o.ExternalID] = o.Email
	}
	want := map[string]string{"1": "alice@example.com", "2": "bob@example.com"}
	if len(got) != len(want) {
		t.Fatalf("Owners returned %d entries across pages, want %d: %v", len(got), len(want), got)
	}
	for id, email := range want {
		if got[id] != email {
			t.Errorf("Owners[%s] = %q, want %q", id, got[id], email)
		}
	}
}

// TestAdapterUnmappedObjectClassErrorsCleanly exercises the "no Mapping
// declared" path (mapping_hs.go's objectMappings — contacts/companies/
// deals/engagements/leads, per design.md §9) for every method that
// resolves a mapping: it must return apperrors.ErrUnsupportedBySoR,
// never panic — an object class outside that set (e.g. HubSpot tickets,
// never in §9's scope) must not crash the mirror sync loop.
func TestAdapterUnmappedObjectClassErrorsCleanly(t *testing.T) {
	client := hubspot.NewClient("us", "test-token")
	adapter := hubspot.NewAdapter(client)

	if _, err := adapter.Backfill(t.Context(), "tickets", ""); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Backfill err = %v, want errors.Is(_, ErrUnsupportedBySoR)", err)
	}
	if _, err := adapter.Modified(t.Context(), "tickets", time.Now(), ""); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Modified err = %v, want errors.Is(_, ErrUnsupportedBySoR)", err)
	}
	if _, err := adapter.Get(t.Context(), "tickets", "1"); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Get err = %v, want errors.Is(_, ErrUnsupportedBySoR)", err)
	}
}

// TestAdapterName pins the fixed-value Name method the compose layer
// selects the incumbent implementation by.
func TestAdapterName(t *testing.T) {
	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token"))
	if adapter.Name() != "hubspot" {
		t.Fatalf("Name() = %q, want hubspot", adapter.Name())
	}
}
