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
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
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
			_ = json.NewDecoder(r.Body).Decode(&patchBody)
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

// writeRequest is the v3 create/update request envelope, for asserting the
// properties the adapter POSTed/PATCHed.
type writeRequest struct {
	Properties map[string]string `json:"properties"`
}
