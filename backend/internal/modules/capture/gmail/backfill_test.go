// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

// The bounded-backfill seam (ADR-0063): the window boundary renders as
// Gmail's inclusive after: operator, the estimate is the provider's own
// number passed through untouched, and a page walk lands each message
// through the same captureOne discipline as incremental sync — a fetch
// fault stops the page without advancing so the committed token stays the
// resume point.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// staleOAuth mints no token — the revoked-refresh-token shape.
type staleOAuth struct{}

func (staleOAuth) AuthCodeURL(state, _ string) string { return "https://auth?state=" + state }
func (staleOAuth) Exchange(context.Context, string, string) (string, error) {
	return "", errors.New("gmail: exchange refused")
}

func (staleOAuth) AccessToken(context.Context, string) (string, error) {
	return "", errors.New("gmail: refresh token revoked")
}

// pagedAPI serves ListAfter in two fixed pages so the walk's token
// hand-over is observable; the other API methods are the sync path's and
// deliberately absent from backfill tests.
type pagedAPI struct {
	fakeAPI
	estimate    int
	estimateErr error
	pages       map[string][]string // pageToken -> ids ("" is the first page)
	next        map[string]string   // pageToken -> next token
	listErr     error
}

func (p *pagedAPI) EstimateAfter(context.Context, string, string) (int, error) {
	if p.estimateErr != nil {
		return 0, p.estimateErr
	}
	return p.estimate, nil
}

func (p *pagedAPI) ListAfter(_ context.Context, _ string, _ string, pageToken string, _ int) ([]string, string, error) {
	if p.listErr != nil {
		return nil, "", p.listErr
	}
	return p.pages[pageToken], p.next[pageToken], nil
}

type failingSink struct{ err error }

func (s failingSink) Upsert(context.Context, connector.NormalizedRecord) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, s.err
}

func TestAfterQueryRendersGmailDateOperator(t *testing.T) {
	after := time.Date(2026, time.January, 5, 23, 30, 0, 0, time.UTC)
	if got := afterQuery(after); got != "after:2026/01/05" {
		t.Fatalf("afterQuery = %q, want after:2026/01/05", got)
	}
}

func TestEstimateBackfillPassesProviderNumberThrough(t *testing.T) {
	c := New(fakeOAuth{access: "access-1"}, &pagedAPI{estimate: 4321})
	got, err := c.EstimateBackfill(context.Background(), authBytes(t), time.Now())
	if err != nil || got != 4321 {
		t.Fatalf("EstimateBackfill = %d, %v — want the provider's 4321 untouched", got, err)
	}
}

func TestEstimateBackfillRefusesMalformedAuth(t *testing.T) {
	c := New(fakeOAuth{}, &pagedAPI{})
	if _, err := c.EstimateBackfill(context.Background(), connector.Auth("{not json"), time.Now()); err == nil {
		t.Fatal("malformed auth state must refuse, not estimate")
	}
}

func TestEstimateBackfillSurfacesTokenFailure(t *testing.T) {
	c := New(staleOAuth{}, &pagedAPI{estimate: 10})
	if _, err := c.EstimateBackfill(context.Background(), authBytes(t), time.Now()); err == nil {
		t.Fatal("a revoked refresh token must surface, not report an estimate")
	}
}

func TestEstimateBackfillSurfacesProviderFailure(t *testing.T) {
	c := New(fakeOAuth{access: "access-1"}, &pagedAPI{estimateErr: errors.New("quota")})
	if _, err := c.EstimateBackfill(context.Background(), authBytes(t), time.Now()); err == nil {
		t.Fatal("a provider fault must surface, not report a zero estimate")
	}
}

func TestBackfillPageCapturesAndCountsHonestly(t *testing.T) {
	api := &pagedAPI{
		pages: map[string][]string{"": {"m1@mail.gmail.com", "m2@mail.gmail.com"}, "tok2": {"m3@mail.gmail.com"}},
		next:  map[string]string{"": "tok2"},
	}
	// m2 is unparseable → an honest skip, never a fatal page error.
	api.raws = map[string][]byte{
		"m1@mail.gmail.com": rawMsg("m1@mail.gmail.com", "alice@acme.com"),
		"m2@mail.gmail.com": []byte("not an rfc822 message"),
		"m3@mail.gmail.com": rawMsg("m3@mail.gmail.com", "bob@acme.com"),
	}
	c := New(fakeOAuth{access: "access-1"}, api)
	sink := &recordingSink{}

	res, err := c.BackfillPage(context.Background(), authBytes(t), time.Now(), "", sink)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if res.Scanned != 2 || res.Captured != 1 || res.Skipped != 1 || res.NextToken != "tok2" {
		t.Fatalf("page 1 = %+v, want scanned 2 / captured 1 / skipped 1 / next tok2", res)
	}

	res, err = c.BackfillPage(context.Background(), authBytes(t), time.Now(), "tok2", sink)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if res.Scanned != 1 || res.Captured != 1 || res.NextToken != "" {
		t.Fatalf("page 2 = %+v, want the final page with no next token", res)
	}
	if len(sink.recs) != 2 {
		t.Fatalf("sink saw %d records, want the 2 parseable messages", len(sink.recs))
	}
}

func TestBackfillPageStopsWithoutAdvancingOnFetchFault(t *testing.T) {
	api := &pagedAPI{pages: map[string][]string{"": {"m1@mail.gmail.com"}}}
	api.getErr = errors.New("transient 503")
	c := New(fakeOAuth{access: "access-1"}, api)
	if _, err := c.BackfillPage(context.Background(), authBytes(t), time.Now(), "", &recordingSink{}); err == nil {
		t.Fatal("a fetch fault must stop the page so the committed token is retried")
	}
}

func TestBackfillPageSurfacesSinkFault(t *testing.T) {
	api := &pagedAPI{pages: map[string][]string{"": {"m1@mail.gmail.com"}}}
	api.raws = map[string][]byte{"m1@mail.gmail.com": rawMsg("m1@mail.gmail.com", "alice@acme.com")}
	c := New(fakeOAuth{access: "access-1"}, api)
	if _, err := c.BackfillPage(context.Background(), authBytes(t), time.Now(), "", failingSink{err: errors.New("db down")}); err == nil {
		t.Fatal("a real sink write fault must stop the page, not be counted as skipped")
	}
}

func TestBackfillPageRefusesMalformedAuthAndStaleToken(t *testing.T) {
	c := New(fakeOAuth{}, &pagedAPI{})
	if _, err := c.BackfillPage(context.Background(), connector.Auth("{not json"), time.Now(), "", &recordingSink{}); err == nil {
		t.Fatal("malformed auth state must refuse")
	}
	if _, err := New(staleOAuth{}, &pagedAPI{}).BackfillPage(context.Background(), authBytes(t), time.Now(), "", &recordingSink{}); err == nil {
		t.Fatal("a revoked refresh token must surface")
	}
}

func TestBackfillPageSurfacesListFault(t *testing.T) {
	c := New(fakeOAuth{access: "access-1"}, &pagedAPI{listErr: errors.New("quota")})
	if _, err := c.BackfillPage(context.Background(), authBytes(t), time.Now(), "", &recordingSink{}); err == nil {
		t.Fatal("a list fault must surface, not read as an empty page")
	}
}

func TestHTTPAPIEstimateAfterReadsResultSizeEstimate(t *testing.T) {
	var gotQuery url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if err := json.NewEncoder(w).Encode(map[string]any{"resultSizeEstimate": 777}); err != nil {
			t.Errorf("encode: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	api := NewAPI(srv.Client(), srv.URL)
	got, err := api.EstimateAfter(context.Background(), "tok", "after:2026/01/05")
	if err != nil || got != 777 {
		t.Fatalf("EstimateAfter = %d, %v — want Google's 777", got, err)
	}
	if gotQuery.Get("q") != "after:2026/01/05" || gotQuery.Get("maxResults") != "1" {
		t.Fatalf("estimate query = %v, want the after: filter at maxResults=1 (count only, no page)", gotQuery)
	}
}

func TestHTTPAPIListAfterPagesWithToken(t *testing.T) {
	var gotQuery url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if err := json.NewEncoder(w).Encode(map[string]any{
			"messages":      []map[string]string{{"id": "m1"}, {"id": "m2"}},
			"nextPageToken": "tok-next",
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	api := NewAPI(srv.Client(), srv.URL)
	ids, next, err := api.ListAfter(context.Background(), "tok", "after:2026/01/05", "tok-prev", 100)
	if err != nil {
		t.Fatalf("ListAfter: %v", err)
	}
	if len(ids) != 2 || ids[0] != "m1" || next != "tok-next" {
		t.Fatalf("ListAfter = %v next=%q, want the page ids and the follow-on token", ids, next)
	}
	if gotQuery.Get("pageToken") != "tok-prev" || gotQuery.Get("maxResults") != "100" {
		t.Fatalf("list query = %v, want the resume token and page size", gotQuery)
	}
}

func TestHTTPAPIListAfterFirstPageOmitsToken(t *testing.T) {
	var gotQuery url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if err := json.NewEncoder(w).Encode(map[string]any{}); err != nil {
			t.Errorf("encode: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	api := NewAPI(srv.Client(), srv.URL)
	ids, next, err := api.ListAfter(context.Background(), "tok", "after:2026/01/05", "", 50)
	if err != nil || len(ids) != 0 || next != "" {
		t.Fatalf("empty window = %v/%q/%v, want an honest empty page", ids, next, err)
	}
	if _, present := gotQuery["pageToken"]; present {
		t.Fatal("a first page must not send an empty pageToken")
	}
}
