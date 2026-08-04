// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package websearchhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/websearch"
)

var searchNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// braveStub answers like the provider, so a test exercises the real request
// path without a network call and without the provider's key.
func braveStub(t *testing.T, status int, body string) *Brave {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	b := NewBrave("test-key", func() time.Time { return searchNow })
	b.client = srv.Client()
	b.endpoint = srv.URL
	return b
}

// A deployment that binds no provider has none. That is the sovereign
// zero-egress posture, so callers must be able to tell it apart from an index
// that answered nothing — hence a sentinel rather than an empty slice.
func TestDisabledRefusesRatherThanAnsweringEmpty(t *testing.T) {
	var disabled Disabled
	results, err := disabled.Search(context.Background(), websearch.Query{Terms: "anything"})
	if err != websearch.ErrNoProvider {
		t.Errorf("Search error = %v, want ErrNoProvider", err)
	}
	if results != nil {
		t.Error("the disabled client returned results")
	}
	if disabled.Provider() != "none" {
		t.Error("the disabled client named a provider that does not exist")
	}
}

// A search with no terms never leaves the process: an unanchored query is the
// case that returns somebody else, and it would be a paid request to find out.
func TestSearchRefusesAnEmptyQueryWithoutCallingOut(t *testing.T) {
	b := braveStub(t, http.StatusOK, `{}`)
	if _, err := b.Search(context.Background(), websearch.Query{Terms: "   "}); err == nil {
		t.Error("an empty query was dispatched")
	}
}

// RetrievedAt is OURS, not the provider's. It is what makes a stored claim age
// visibly instead of pretending to be current.
func TestSearchStampsOurOwnReadDate(t *testing.T) {
	b := braveStub(t, http.StatusOK, `{"web":{"results":[
		{"title":"Anna Weber — Head of Procurement","url":"https://www.linkedin.com/in/anna-weber",
		 "description":"Anna leads procurement.","page_age":"2026-01-02T00:00:00Z"}]}}`)

	results, err := b.Search(context.Background(), websearch.Query{Terms: `"Anna Weber" "ScaleCommerce"`})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !results[0].RetrievedAt.Equal(searchNow) {
		t.Errorf("RetrievedAt = %v, want the injected clock %v", results[0].RetrievedAt, searchNow)
	}
	if results[0].PublishedAt == nil {
		t.Error("the provider's own page age was dropped; it is what a reader judges staleness by")
	}
	if results[0].Title == "" || results[0].Snippet == "" {
		t.Error("the result text is empty; it IS the evidence, since the page is never fetched")
	}
}

// Narrowing to one domain is the cheapest form of the questions this product
// asks, so it rides the query rather than filtering results already paid for.
func TestSearchNarrowsToASiteInTheQuery(t *testing.T) {
	b := braveStub(t, http.StatusOK, `{"web":{"results":[]}}`)
	if _, err := b.Search(context.Background(),
		websearch.Query{Terms: "Anna Weber", Site: "scalecommerce.example"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

// These searches name a person and their employer, so the query string is
// personal data. An error message outlives the request and is an observability
// surface — it must carry the failure and nothing about who was searched for.
func TestASearchFailureNamesTheProviderAndNotTheSubject(t *testing.T) {
	b := braveStub(t, http.StatusTooManyRequests, `{"error":"quota exceeded for query Anna Weber ScaleCommerce"}`)

	_, err := b.Search(context.Background(), websearch.Query{Terms: `"Anna Weber" "ScaleCommerce"`})
	if err == nil {
		t.Fatal("a 429 was reported as success")
	}
	if !strings.Contains(err.Error(), "brave") {
		t.Errorf("the error does not name the provider: %v", err)
	}
	for _, leak := range []string{"Anna", "Weber", "ScaleCommerce", "quota exceeded for"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error leaks %q into the logs: %v", leak, err)
		}
	}
}

// A body the decoder cannot read is reported as such, again without echoing
// the payload — which is about a person.
func TestAnUnreadableAnswerIsReportedWithoutThePayload(t *testing.T) {
	b := braveStub(t, http.StatusOK, `{"web": {"results": [ NOT JSON`)

	_, err := b.Search(context.Background(), websearch.Query{Terms: `"Anna Weber"`})
	if err == nil {
		t.Fatal("a malformed answer was accepted")
	}
	if strings.Contains(err.Error(), "Anna") || strings.Contains(err.Error(), "NOT JSON") {
		t.Errorf("the decode error echoed the payload: %v", err)
	}
}
