// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsJSONRecognisesTheStructuredSuffix(t *testing.T) {
	for mediaType, want := range map[string]bool{
		"application/json":         true,
		"text/json":                true,
		"application/vnd.api+json": true,
		"text/html":                false,
		"text/markdown":            false,
		"":                         false,
		"application/jsonwhatever": false,
	} {
		if got := (Doc{MediaType: mediaType}).IsJSON(); got != want {
			t.Errorf("Doc{%q}.IsJSON() = %v, want %v", mediaType, got, want)
		}
	}
}

// StripTags is an HTML reducer: every '>' becomes a space and anything between
// '<' and '>' is swallowed. Both occur inside ordinary JSON string values, so a
// caller that parsed the reduced text would be parsing something the server
// never sent.
//
// This drives the REAL Fetch, not a hand-built Doc: a test that constructed its
// own Doc would stay green if the JSON branch were deleted and JSON went back
// through StripTags.
func TestFetchServesJSONVerbatim(t *testing.T) {
	const body = `{"data":[{"id":"a","modality":"text->text","desc":"see <https://x.test/> for more"}]}`

	// The reduction this test exists to prevent must actually damage the body,
	// or the assertion below proves nothing.
	if StripTags(body) == body {
		t.Fatal("StripTags left this body unchanged; the fixture no longer exercises the hazard")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	doc, err := newFetcher(http.DefaultTransport).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.IsJSON() {
		t.Fatalf("MediaType = %q, want it recognised as JSON", doc.MediaType)
	}
	if doc.Text != body {
		t.Errorf("Fetch rewrote the server's JSON:\n got %q\nwant %q", doc.Text, body)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(doc.Text), &parsed); err != nil {
		t.Fatalf("fetched JSON must remain parseable: %v", err)
	}
}

// HTML is still reduced — the JSON branch must not have widened into a
// pass-through for everything.
func TestFetchStillReducesHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
		_, _ = w.Write([]byte("<p>Aurora Large</p>"))
	}))
	defer srv.Close()

	doc, err := newFetcher(http.DefaultTransport).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.Contains(doc.Text, "<p>") {
		t.Errorf("HTML must still be reduced, got %q", doc.Text)
	}
	if !strings.Contains(doc.Text, "Aurora Large") {
		t.Errorf("the reduction must keep the text, got %q", doc.Text)
	}
}
