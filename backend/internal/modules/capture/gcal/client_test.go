// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// calendarStub routes the Calendar v3 endpoints the client calls onto canned
// responses, so the REST/paging/sync-token logic is proven with no network.
func calendarStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/calendars/primary", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": "rep@myco.com"})
	})

	mux.HandleFunc("/calendars/primary/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// An expired syncToken → 410 Gone (mapped to ErrSyncTokenGone).
		if q.Get("syncToken") == "stale" {
			w.WriteHeader(http.StatusGone)
			return
		}
		// Second page requested → return the terminal page carrying the token.
		if q.Get("pageToken") == "page-2" {
			writeJSON(w, map[string]any{
				"items":         []map[string]any{{"id": "evt-2", "status": "confirmed"}},
				"nextSyncToken": "sync-final",
			})
			return
		}
		// First page → one event + a nextPageToken (proves paging is walked).
		writeJSON(w, map[string]any{
			"items":         []map[string]any{{"id": "evt-1", "status": "confirmed"}},
			"nextPageToken": "page-2",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

//craft:ignore naked-any v is an arbitrary canned JSON response body for the stub
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors test stub write; an encode failure surfaces as the client-side decode error the assertion checks
	_ = json.NewEncoder(w).Encode(v)
}

func newTestAPI(t *testing.T) API {
	srv := calendarStub(t)
	return NewAPI(srv.Client(), srv.URL)
}

func TestPrimaryOwnerReturnsCalendarAddress(t *testing.T) {
	api := newTestAPI(t)
	owner, err := api.PrimaryOwner(context.Background(), "access-1")
	if err != nil {
		t.Fatalf("PrimaryOwner: %v", err)
	}
	if owner != "rep@myco.com" {
		t.Errorf("owner = %q, want rep@myco.com", owner)
	}
}

func TestListInitialWalksPagesAndReturnsSyncToken(t *testing.T) {
	api := newTestAPI(t)
	events, token, err := api.ListInitial(context.Background(), "access-1")
	if err != nil {
		t.Fatalf("ListInitial: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("collected %d events across pages, want 2", len(events))
	}
	if token != "sync-final" {
		t.Errorf("syncToken = %q, want sync-final (from the terminal page)", token)
	}
	// The raw event bytes must reach the caller intact (evidence, memory-first).
	if !json.Valid(events[0]) {
		t.Errorf("event 0 is not valid JSON: %s", events[0])
	}
}

func TestListIncrementalReturnsEventsAndToken(t *testing.T) {
	api := newTestAPI(t)
	events, token, err := api.ListIncremental(context.Background(), "access-1", "fresh")
	if err != nil {
		t.Fatalf("ListIncremental: %v", err)
	}
	if len(events) != 2 || token != "sync-final" {
		t.Errorf("got %d events / token %q, want 2 / sync-final", len(events), token)
	}
}

func TestListIncrementalExpiredTokenMapsGoneSentinel(t *testing.T) {
	api := newTestAPI(t)
	if _, _, err := api.ListIncremental(context.Background(), "access-1", "stale"); !errors.Is(err, ErrSyncTokenGone) {
		t.Fatalf("want ErrSyncTokenGone for an expired token, got %v", err)
	}
}

func TestListInitialRejectsMissingTerminalSyncToken(t *testing.T) {
	// A fully-walked list that never yields a nextSyncToken is a Google
	// contract violation; the client must surface it (retryable) rather than
	// return an empty cursor that would force a full re-backfill every cycle.
	mux := http.NewServeMux()
	mux.HandleFunc("/calendars/primary/events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"items": []map[string]any{{"id": "evt-1", "status": "confirmed"}},
			// no nextPageToken (terminal) and no nextSyncToken (the violation)
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api := NewAPI(srv.Client(), srv.URL)
	if _, _, err := api.ListInitial(context.Background(), "access-1"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable for a terminal page with no syncToken, got %v", err)
	}
}

func TestGetMapsAuthAndTransportSentinels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/calendars/primary", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api := NewAPI(srv.Client(), srv.URL)
	if _, err := api.PrimaryOwner(context.Background(), "access-1"); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for a 403, got %v", err)
	}
}
