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

	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// TestAdapterCreatePostsMappedProps: a canonical person Create projects onto
// contacts firstname/lastname (OVA-MAP-W1), POSTs to /crm/v3/objects/contacts,
// and maps the created record back to canonical.
func TestAdapterCreatePostsMappedProps(t *testing.T) {
	var gotBody writeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/crm/v3/objects/contacts" {
			t.Fatalf("got %s %s, want POST /crm/v3/objects/contacts", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding POST body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"555","properties":{"hs_object_id":"555",
			"firstname":"Ada","lastname":"Lovelace","lastmodifieddate":"2026-05-01T00:00:00Z"}}`))
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	rec, err := adapter.Create(t.Context(), "person", map[string]any{
		"first_name": "Ada", "last_name": "Lovelace",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotBody.Properties["firstname"] != "Ada" || gotBody.Properties["lastname"] != "Lovelace" {
		t.Errorf("POSTed properties = %#v, want firstname=Ada lastname=Lovelace", gotBody.Properties)
	}
	if rec.ExternalID != "555" || rec.Fields["first_name"] != "Ada" {
		t.Errorf("mapped record = %+v, want ExternalID 555 + first_name Ada", rec)
	}
}

// TestAdapterUpdateRefusesOnBaselineDrift (AC-OV-4): when the incumbent's
// current record is newer than the stored baseline, the write is refused with
// ErrVersionSkew and NO PATCH is issued — the incumbent wins, never a blind
// overwrite.
func TestAdapterUpdateRefusesOnBaselineDrift(t *testing.T) {
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/crm/v3/objects/contacts/batch/read":
			w.Header().Set("Content-Type", "application/json")
			// Current incumbent lastmodifieddate is AFTER the caller's baseline.
			_, _ = w.Write([]byte(`{"results":[{"id":"555","properties":{"hs_object_id":"555",
				"lastmodifieddate":"2026-06-01T00:00:00Z"}}]}`))
		case r.Method == http.MethodPatch:
			patched = true
			t.Error("PATCH must not be issued when the baseline has drifted")
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	baseline := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // older than the current 2026-06-01
	_, err := adapter.Update(t.Context(), "person", "555", map[string]any{"first_name": "Ada2"}, baseline)
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("Update on drift: err = %v, want ErrVersionSkew", err)
	}
	if patched {
		t.Error("a drifted update must not PATCH")
	}
}

// TestAdapterUpdateAppliesWhenBaselineFresh: baseline equals the incumbent's
// current lastmodifieddate (no third-party change) → the PATCH goes through
// with the mapped changed property.
func TestAdapterUpdateAppliesWhenBaselineFresh(t *testing.T) {
	var patchBody writeRequest
	baseline := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/crm/v3/objects/contacts/batch/read":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"id":"555","properties":{"hs_object_id":"555",
				"lastmodifieddate":"2026-05-01T00:00:00Z"}}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/crm/v3/objects/contacts/555":
			if err := json.NewDecoder(r.Body).Decode(&patchBody); err != nil {
				t.Errorf("decoding PATCH body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"555","properties":{"hs_object_id":"555",
				"firstname":"Ada2","lastmodifieddate":"2026-06-02T00:00:00Z"}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	rec, err := adapter.Update(t.Context(), "person", "555", map[string]any{"first_name": "Ada2"}, baseline)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if patchBody.Properties["firstname"] != "Ada2" {
		t.Errorf("PATCHed properties = %#v, want firstname=Ada2", patchBody.Properties)
	}
	if rec.Fields["first_name"] != "Ada2" {
		t.Errorf("mapped record first_name = %v, want Ada2", rec.Fields["first_name"])
	}
}

// TestAdapterArchiveDeletes: Archive resolves the incumbent class and DELETEs
// the object.
func TestAdapterArchiveDeletes(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	if err := adapter.Archive(t.Context(), "person", "555"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if deleted != "/crm/v3/objects/contacts/555" {
		t.Errorf("DELETE path = %q, want /crm/v3/objects/contacts/555", deleted)
	}
}

// TestAdapterArchiveActivityResolvesClassFromNamespacedID: an activity's
// mirror id is "<class>:<id>" (OVA-MAP-7); Archive recovers the engagement
// class from the prefix and DELETEs the raw id on that class's endpoint.
func TestAdapterArchiveActivityResolvesClassFromNamespacedID(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	if err := adapter.Archive(t.Context(), "activity", "calls:123"); err != nil {
		t.Fatalf("Archive activity: %v", err)
	}
	if !strings.HasSuffix(deleted, "/crm/v3/objects/calls/123") {
		t.Errorf("DELETE path = %q, want .../crm/v3/objects/calls/123", deleted)
	}
}

// TestAdapterCreateRejectsAllReadOnlyFields: a create whose every supplied
// field is read-only/derived (a person with only full_name) cannot create an
// incumbent object — it errors and never POSTs a blank record.
func TestAdapterCreateRejectsAllReadOnlyFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no HTTP call expected, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	if _, err := adapter.Create(t.Context(), "person", map[string]any{"full_name": "Ada Lovelace"}); err == nil {
		t.Error("Create with only read-only fields must error, not POST a blank record")
	}
}

// TestAdapterUpdateNoOpWhenOnlyReadOnlyFields: a patch of only read-only
// fields writes nothing — it returns the current record via the drift-anchor
// read and never PATCHes.
func TestAdapterUpdateNoOpWhenOnlyReadOnlyFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			t.Error("a read-only-only patch must not PATCH")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"555","properties":{"hs_object_id":"555",
			"firstname":"Ada","lastmodifieddate":"2026-05-01T00:00:00Z"}}]}`))
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	rec, err := adapter.Update(t.Context(), "person", "555", map[string]any{"full_name": "Ada Renamed"}, time.Now())
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if rec.Fields["first_name"] != "Ada" {
		t.Errorf("no-op Update should return the current record, got %+v", rec.Fields)
	}
}

// TestAdapterArchiveRejectsUnknownClass: a canonical class that maps to no
// single incumbent write class is an honest error, not a guessed endpoint.
func TestAdapterArchiveRejectsUnknownClass(t *testing.T) {
	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok"))
	if err := adapter.Archive(t.Context(), "widget", "1"); err == nil {
		t.Error("Archive of an unknown canonical class must error")
	}
}

// TestAdapterArchiveRejectsUnprefixedActivityID: an activity id with no
// "<class>:" prefix cannot recover its engagement class (OVA-MAP-7) — error,
// never a guessed class.
func TestAdapterArchiveRejectsUnprefixedActivityID(t *testing.T) {
	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok"))
	if err := adapter.Archive(t.Context(), "activity", "123"); err == nil {
		t.Error("Archive of an un-namespaced activity id must error")
	}
}

// TestAdapterCreateSurfacesIncumbentError: a non-2xx from HubSpot on the
// create POST surfaces as a clean sentinel (no HubSpot body leaked), the
// write-transport error path.
func TestAdapterCreateSurfacesIncumbentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom","category":"INTERNAL"}`))
	}))
	defer srv.Close()

	adapter := hubspot.NewAdapter(hubspot.NewClient("us", "tok", hubspot.WithBaseURL(srv.URL)))
	_, err := adapter.Create(t.Context(), "person", map[string]any{"first_name": "Ada"})
	if err == nil {
		t.Fatal("Create against a 5xx incumbent must error")
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("error leaks the HubSpot message: %v", err)
	}
}

// writeRequest is the v3 create/update request envelope, for asserting the
// properties the adapter POSTed/PATCHed.
type writeRequest struct {
	Properties map[string]string `json:"properties"`
}
