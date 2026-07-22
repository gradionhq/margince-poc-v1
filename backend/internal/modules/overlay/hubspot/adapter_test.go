// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// archivedContactsJSON is a design §11 list response for the archived
// (deleted) contacts feed — GET .../objects/contacts?archived=true — each
// object carrying its archivedAt timestamp, ascending, with a paging
// cursor.
const archivedContactsJSON = `{
  "results": [
    { "id": "100214862042", "properties": { "hs_object_id": "100214862042" },
      "createdAt": "2024-11-15T13:27:49.194Z", "updatedAt": "2026-05-13T06:44:38.727Z",
      "archived": true, "archivedAt": "2026-06-01T10:00:00.000Z" },
    { "id": "100214862099", "properties": { "hs_object_id": "100214862099" },
      "createdAt": "2024-11-15T13:27:49.194Z", "updatedAt": "2026-05-14T09:12:01.100Z",
      "archived": true, "archivedAt": "2026-06-02T11:30:00.000Z" }
  ],
  "paging": { "next": { "after": "200" } }
}`

// TestAdapterDeletionsMapsArchivedRecords drives the Adapter's Deletions
// method against the archived-object list feed: it must request the
// object's list endpoint with archived=true, map each archived record
// into an overlay.Deletion keyed by the CANONICAL object class (contacts
// → person, never the incumbent source name), carry archivedAt through as
// DeletedAt, and propagate the paging cursor.
func TestAdapterDeletionsMapsArchivedRecords(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(archivedContactsJSON)); err != nil {
			t.Errorf("writing response body: %v", err)
		}
	}))
	defer srv.Close()

	client := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	adapter := hubspot.NewAdapter(client)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	page, err := adapter.Deletions(t.Context(), "contacts", since, "")
	if err != nil {
		t.Fatalf("Deletions: unexpected error: %v", err)
	}
	if gotPath != "/crm/v3/objects/contacts" {
		t.Fatalf("path = %q, want /crm/v3/objects/contacts", gotPath)
	}
	if !strings.Contains(gotQuery, "archived=true") {
		t.Fatalf("query = %q, want it to carry archived=true", gotQuery)
	}
	if len(page.Deletions) != 2 {
		t.Fatalf("len(Deletions) = %d, want 2", len(page.Deletions))
	}
	first := page.Deletions[0]
	if first.ExternalID != "100214862042" {
		t.Errorf("Deletions[0].ExternalID = %q, want 100214862042", first.ExternalID)
	}
	if first.ObjectClass != "person" {
		t.Errorf("Deletions[0].ObjectClass = %q, want the canonical person", first.ObjectClass)
	}
	wantDeletedAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if !first.DeletedAt.Equal(wantDeletedAt) {
		t.Errorf("Deletions[0].DeletedAt = %v, want %v", first.DeletedAt, wantDeletedAt)
	}
	if page.NextCursor != "200" {
		t.Errorf("NextCursor = %q, want 200", page.NextCursor)
	}
}

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

// TestAdapterEnrichLeadsDerivesContactFields is the OVA-MAP-5 golden proof:
// a lead's full_name comes from the real hs_lead_name property, and its
// email/company_name are denormalized from the contact reached through the
// lead's required contact association — never from non-existent lead
// properties.
func TestAdapterEnrichLeadsDerivesContactFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/crm/v3/objects/leads":
			_, _ = w.Write([]byte(`{"results":[{"id":"7701","properties":{
				"hs_object_id":"7701","hs_lastmodifieddate":"2026-06-03T00:00:00.000Z",
				"hs_lead_name":"Erika Musterfrau","hubspot_owner_id":"owner-3"}}]}`))
		case "/crm/v4/objects/leads/7701/associations/contacts":
			_, _ = w.Write([]byte(`{"results":[{"toObjectId":"555","associationTypes":[{"category":"HUBSPOT_DEFINED","typeId":1}]}]}`))
		case "/crm/v3/objects/contacts/batch/read":
			_, _ = w.Write([]byte(`{"results":[{"id":"555","properties":{
				"email":"Erika@Example.DE","company":"Musterfrau Consulting"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL)))
	page, err := adapter.Backfill(t.Context(), "leads", "")
	if err != nil {
		t.Fatalf("Backfill(leads): %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(page.Records))
	}
	f := page.Records[0].Fields
	if f["full_name"] != "Erika Musterfrau" {
		t.Errorf("full_name = %v, want Erika Musterfrau (from hs_lead_name)", f["full_name"])
	}
	if f["email"] != "erika@example.de" {
		t.Errorf("email = %v, want erika@example.de (lowercased, from the associated contact)", f["email"])
	}
	if f["company_name"] != "Musterfrau Consulting" {
		t.Errorf("company_name = %v, want the associated contact's company", f["company_name"])
	}
}

// TestAdapterGetEnrichesLeadContactFields proves the force-fresh single-record
// path returns the SAME shape as backfill (OVA-MAP-5): Get("leads", ...) also
// denormalizes the associated contact's email/company_name, not a bare lead.
func TestAdapterGetEnrichesLeadContactFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/crm/v3/objects/leads/batch/read":
			_, _ = w.Write([]byte(`{"results":[{"id":"7701","properties":{
				"hs_object_id":"7701","hs_lastmodifieddate":"2026-06-03T00:00:00.000Z","hs_lead_name":"Erika Musterfrau"}}]}`))
		case "/crm/v4/objects/leads/7701/associations/contacts":
			_, _ = w.Write([]byte(`{"results":[{"toObjectId":"555","associationTypes":[{"category":"HUBSPOT_DEFINED","typeId":1}]}]}`))
		case "/crm/v3/objects/contacts/batch/read":
			_, _ = w.Write([]byte(`{"results":[{"id":"555","properties":{"email":"erika@example.de","company":"Musterfrau Consulting"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL)))
	rec, err := adapter.Get(t.Context(), "leads", "7701")
	if err != nil {
		t.Fatalf("Get(leads): %v", err)
	}
	if rec.Fields["email"] != "erika@example.de" {
		t.Errorf("email = %v, want the associated contact's email (force-fresh must enrich like backfill)", rec.Fields["email"])
	}
	if rec.Fields["company_name"] != "Musterfrau Consulting" {
		t.Errorf("company_name = %v, want the associated contact's company", rec.Fields["company_name"])
	}
}

// TestAdapterEnrichLeadsLeavesFieldsAbsentWithoutAssociation proves a lead
// with no contact association keeps email/company_name absent (OVA-MAP-5:
// null rather than invented), and never calls the contact batch-read.
func TestAdapterEnrichLeadsLeavesFieldsAbsentWithoutAssociation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/crm/v3/objects/leads":
			_, _ = w.Write([]byte(`{"results":[{"id":"7702","properties":{"hs_object_id":"7702","hs_lastmodifieddate":"2026-06-03T00:00:00.000Z","hs_lead_name":"No Contact"}}]}`))
		case r.URL.Path == "/crm/v4/objects/leads/7702/associations/contacts":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "/batch/read"):
			t.Errorf("batch/read must not be called when a lead has no contact association")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL)))
	page, err := adapter.Backfill(t.Context(), "leads", "")
	if err != nil {
		t.Fatalf("Backfill(leads): %v", err)
	}
	f := page.Records[0].Fields
	if _, present := f["email"]; present {
		t.Errorf("email = %v, want absent for a lead with no contact association", f["email"])
	}
	if _, present := f["company_name"]; present {
		t.Errorf("company_name = %v, want absent for a lead with no contact association", f["company_name"])
	}
}

// TestAdapterAssociationsNamespacesEngagementEndpoints proves an
// engagement-to-engagement edge namespaces BOTH endpoints (OVA-MAP-7): the
// stored edge references the namespaced mirror ids so it joins/purges with
// the activity mirror rows, and both endpoint types canonicalize to
// "activity". The from side arrives already-namespaced (the mirror id
// Backfill hands in); the query hits the raw object id; the to side is
// namespaced from the raw association target.
func TestAdapterAssociationsNamespacesEngagementEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v4/objects/calls/123/associations/meetings" {
			t.Fatalf("path = %q, want the RAW from id /crm/v4/objects/calls/123/associations/meetings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [ { "toObjectId": 456,
			  "associationTypes": [ {"category":"HUBSPOT_DEFINED","typeId":9,"label":"Related"} ] } ]
		}`))
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL)))
	// fromID arrives namespaced (as the mirror hands it back); the query above
	// asserts it was stripped to the raw "123" for the API call.
	assocs, err := adapter.Associations(t.Context(), "calls", "calls:123", "meetings")
	if err != nil {
		t.Fatalf("Associations: %v", err)
	}
	if len(assocs) != 1 {
		t.Fatalf("len(assocs) = %d, want 1", len(assocs))
	}
	got := assocs[0]
	want := overlay.Assoc{
		FromType: "activity", FromID: "calls:123", ToType: "activity", ToID: "meetings:456",
		TypeID: 9, Category: "HUBSPOT_DEFINED", Label: "Related", Direction: "forward",
	}
	if got != want {
		t.Fatalf("assocs[0] = %#v, want %#v (both engagement endpoints namespaced + canonical type)", got, want)
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
	// Endpoint types are CANONICAL (deal/organization), not the incumbent
	// class names — the stored edge must reference the same identity the
	// mirror rows and PurgeRecord use (OVA-MAP-7 coherence).
	want := overlay.Assoc{
		FromType: "deal", FromID: "123", ToType: "organization", ToID: "61655665850",
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
