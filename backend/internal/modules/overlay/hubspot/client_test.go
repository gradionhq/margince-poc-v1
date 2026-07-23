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

// mustWrite writes body to w from inside an httptest handler goroutine,
// recording a failure via t.Errorf (goroutine-safe, unlike t.Fatalf) if
// the write fails. The repo rule forbids discarding the Write return.
func mustWrite(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("writing response body: %v", err)
	}
}

// roundTripperFunc adapts a plain function to http.RoundTripper so a test
// can inject a deterministic transport-level failure via WithHTTPClient —
// no real socket needed.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// listPageJSON is a design §11-shaped list response: results[] each with
// id+properties, and paging.next.after carrying the id-keyset cursor.
const listPageJSON = `{
  "results": [
    { "id": "100214862042",
      "properties": { "hs_object_id": "100214862042", "firstname": "Christian", "lastname": "Mueller",
        "lastmodifieddate": "2026-05-13T06:44:38.727Z" },
      "createdAt": "2024-11-15T13:27:49.194Z", "updatedAt": "2026-05-13T06:44:38.727Z", "archived": false }
  ],
  "paging": { "next": { "after": "100214862043" } }
}`

func TestClientListParsesPageCursor(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, listPageJSON)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	page, err := c.List(t.Context(), "contacts", []string{"firstname", "lastname"}, "", 100)
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
	if gotPath != "/crm/v3/objects/contacts" {
		t.Fatalf("path = %q, want /crm/v3/objects/contacts", gotPath)
	}
	if len(page.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(page.Results))
	}
	rec := page.Results[0]
	if rec.ID != "100214862042" {
		t.Fatalf("ID = %q, want 100214862042", rec.ID)
	}
	if rec.Properties["firstname"] != "Christian" {
		t.Fatalf("Properties[firstname] = %q, want Christian", rec.Properties["firstname"])
	}
	if page.NextAfter != "100214862043" {
		t.Fatalf("NextAfter = %q, want 100214862043", page.NextAfter)
	}
}

func TestClientListNoNextPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"results":[],"paging":{}}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	page, err := c.List(t.Context(), "contacts", nil, "", 100)
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if page.NextAfter != "" {
		t.Fatalf("NextAfter = %q, want empty", page.NextAfter)
	}
}

func TestClientSearchModifiedSendsSingleSortGTEBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
			return
		}
		if r.URL.Path != "/crm/v3/objects/deals/search" {
			t.Errorf("path = %q, want /crm/v3/objects/deals/search", r.URL.Path)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{
			"total": 1,
			"results": [ { "id": "42", "properties": { "hs_lastmodifieddate": "2026-05-13T06:44:38.727Z" } } ],
			"paging": { "next": { "after": "43" } }
		}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	since := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	page, err := c.SearchModified(t.Context(), "deals", "hs_lastmodifieddate", since, "", 100, []string{"dealname"})
	if err != nil {
		t.Fatalf("SearchModified: unexpected error: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("Total = %d, want 1", page.Total)
	}
	if page.NextAfter != "43" {
		t.Fatalf("NextAfter = %q, want 43", page.NextAfter)
	}

	sorts, ok := gotBody["sorts"].([]any)
	if !ok || len(sorts) != 1 {
		t.Fatalf("sorts = %#v, want exactly one sort (HubSpot Search honors only a single sort)", gotBody["sorts"])
	}
	sort0, ok := sorts[0].(map[string]any)
	if !ok {
		t.Fatalf("sorts[0] = %#v, want a JSON object", sorts[0])
	}
	if sort0["propertyName"] != "hs_lastmodifieddate" {
		t.Fatalf("sorts[0].propertyName = %v, want hs_lastmodifieddate", sort0["propertyName"])
	}

	groups, ok := gotBody["filterGroups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("filterGroups = %#v, want exactly one group", gotBody["filterGroups"])
	}
	group0, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("filterGroups[0] = %#v, want a JSON object", groups[0])
	}
	filters, ok := group0["filters"].([]any)
	if !ok || len(filters) != 1 {
		t.Fatalf("filters = %#v, want exactly one GTE filter", group0["filters"])
	}
	filter0, ok := filters[0].(map[string]any)
	if !ok {
		t.Fatalf("filters[0] = %#v, want a JSON object", filters[0])
	}
	if filter0["operator"] != "GTE" {
		t.Fatalf("filter operator = %v, want GTE", filter0["operator"])
	}
	if filter0["propertyName"] != "hs_lastmodifieddate" {
		t.Fatalf("filter propertyName = %v, want hs_lastmodifieddate", filter0["propertyName"])
	}
}

func TestClientBatchReadParsesResults(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/objects/companies/batch/read" {
			t.Errorf("path = %q, want /crm/v3/objects/companies/batch/read", r.URL.Path)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"results":[{"id":"61655665850","properties":{"name":"Acme GmbH"}}]}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	recs, err := c.BatchRead(t.Context(), "companies", []string{"61655665850"}, []string{"name"})
	if err != nil {
		t.Fatalf("BatchRead: unexpected error: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "61655665850" {
		t.Fatalf("recs = %#v, want one record with id 61655665850", recs)
	}
	if recs[0].Properties["name"] != "Acme GmbH" {
		t.Fatalf("Properties[name] = %q, want Acme GmbH", recs[0].Properties["name"])
	}

	inputs, ok := gotBody["inputs"].([]any)
	if !ok || len(inputs) != 1 {
		t.Fatalf("inputs = %#v, want one input", gotBody["inputs"])
	}
}

func TestClientAssociationsParsesTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v4/objects/deals/123/associations/companies" {
			t.Errorf("path = %q, want /crm/v4/objects/deals/123/associations/companies", r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{
			"results": [ { "toObjectId": 61655665850,
			  "associationTypes": [ {"category":"HUBSPOT_DEFINED","typeId":5,"label":"Primary"},
			                        {"category":"HUBSPOT_DEFINED","typeId":341,"label":null} ] } ]
		}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	assocs, err := c.Associations(t.Context(), "deals", "123", "companies")
	if err != nil {
		t.Fatalf("Associations: unexpected error: %v", err)
	}
	if len(assocs) != 1 {
		t.Fatalf("len(assocs) = %d, want 1", len(assocs))
	}
	if assocs[0].ToObjectID != "61655665850" {
		t.Fatalf("ToObjectID = %q, want 61655665850", assocs[0].ToObjectID)
	}
	if len(assocs[0].Types) != 2 || assocs[0].Types[0].TypeID != 5 || assocs[0].Types[0].Label != "Primary" {
		t.Fatalf("Types = %#v, want [{Primary} {}]", assocs[0].Types)
	}
}

func TestClientAssociationsFollowsPaging(t *testing.T) {
	// Two pages: the first carries paging.next.after, the second does not.
	// Every edge across both pages must be collected — a single-page read
	// would silently lose the >500th edge — and page two must carry the
	// cursor as ?after=.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after") {
		case "":
			mustWrite(t, w, `{
				"results": [ { "toObjectId": 111, "associationTypes": [ {"category":"HUBSPOT_DEFINED","typeId":5,"label":"Primary"} ] } ],
				"paging": { "next": { "after": "page2" } }
			}`)
		case "page2":
			mustWrite(t, w, `{
				"results": [ { "toObjectId": 222, "associationTypes": [ {"category":"HUBSPOT_DEFINED","typeId":5,"label":null} ] } ]
			}`)
		default:
			t.Errorf("unexpected after cursor %q", r.URL.Query().Get("after"))
		}
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	assocs, err := c.Associations(t.Context(), "deals", "123", "companies")
	if err != nil {
		t.Fatalf("Associations: unexpected error: %v", err)
	}
	if len(assocs) != 2 {
		t.Fatalf("len(assocs) = %d, want 2 (both pages collected)", len(assocs))
	}
	if assocs[0].ToObjectID != "111" || assocs[1].ToObjectID != "222" {
		t.Fatalf("ToObjectIDs = [%q %q], want [111 222]", assocs[0].ToObjectID, assocs[1].ToObjectID)
	}
}

func TestClientAssociationsFailsFastOnNonAdvancingCursor(t *testing.T) {
	// A server that always echoes the same next cursor must be caught on the
	// first repeat, not spun to the page cap.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"results":[{"toObjectId":1,"associationTypes":[]}],"paging":{"next":{"after":"stuck"}}}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	if _, err := c.Associations(t.Context(), "deals", "123", "companies"); err == nil {
		t.Fatal("Associations with a non-advancing cursor: want an error, got nil")
	}
	// One page with a real cursor, then one more that repeats it → caught.
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2 (fail fast on the first repeated cursor, not spin to the cap)", calls)
	}
}

func TestClientOwnerParsesEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crm/v3/owners/1197833249" {
			t.Errorf("path = %q, want /crm/v3/owners/1197833249", r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"id":"1197833249","email":"christian@example.de","firstName":"Christian","lastName":"Mueller"}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	owner, err := c.Owner(t.Context(), "1197833249")
	if err != nil {
		t.Fatalf("Owner: unexpected error: %v", err)
	}
	if owner.Email != "christian@example.de" {
		t.Fatalf("Email = %q, want christian@example.de", owner.Email)
	}
}

func TestClientNonSuccessMapsToSentinels(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantErrMsg string // substring the raw provider body must NOT appear in
	}{
		{
			name:    "429 rate limit maps to budget exhausted",
			status:  http.StatusTooManyRequests,
			body:    `{"status":"error","message":"You have reached your daily limit","category":"RATE_LIMITS"}`,
			wantErr: apperrors.ErrIncumbentBudgetExhausted,
		},
		{
			name:    "403 missing scopes maps to permission denied",
			status:  http.StatusForbidden,
			body:    `{"status":"error","message":"This app hasn't been granted all required scopes","category":"MISSING_SCOPES"}`,
			wantErr: apperrors.ErrPermissionDenied,
		},
		{
			name:    "401 unauthorized maps to permission denied",
			status:  http.StatusUnauthorized,
			body:    `{"status":"error","message":"Authentication credentials not found","category":"INVALID_AUTHENTICATION"}`,
			wantErr: apperrors.ErrPermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				mustWrite(t, w, tt.body)
			}))
			defer srv.Close()

			c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

			_, err := c.List(t.Context(), "contacts", nil, "", 100)
			if err == nil {
				t.Fatalf("List: expected an error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("List err = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), tt.body) {
				t.Fatalf("error %q leaks the raw provider body %q", err.Error(), tt.body)
			}
		})
	}
}

// TestClientNonSuccessDefaultsToUnreachable proves mapStatus's default
// branch: a non-2xx status this client maps to no specific sentinel
// (neither 429/401/403 nor a RATE_LIMITS category) answers
// ErrUnreachable, never a raw-status leak or a fabricated success.
func TestClientNonSuccessDefaultsToUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		mustWrite(t, w, `{"status":"error","message":"boom","category":"UNKNOWN"}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	_, err := c.List(t.Context(), "contacts", nil, "", 100)
	if err == nil {
		t.Fatal("List: expected an error, got nil")
	}
	if !errors.Is(err, hubspot.ErrUnreachable) {
		t.Fatalf("List err = %v, want errors.Is(_, ErrUnreachable)", err)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q leaks the raw provider message", err.Error())
	}
}

// TestClientMalformedErrorEnvelopeStillMapsCleanly proves mapStatus's
// best-effort envelope decode: a non-2xx response whose body isn't even
// valid JSON still answers a clean status-derived sentinel rather than
// failing on the decode itself.
func TestClientMalformedErrorEnvelopeStillMapsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		mustWrite(t, w, `not-json`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	_, err := c.List(t.Context(), "contacts", nil, "", 100)
	if !errors.Is(err, apperrors.ErrIncumbentBudgetExhausted) {
		t.Fatalf("List err = %v, want errors.Is(_, ErrIncumbentBudgetExhausted) even with a malformed envelope", err)
	}
}

// TestClientTransportFailureAnswersUnreachable proves a DNS/connection
// failure maps to ErrUnreachable, the transport-level sentinel — distinct
// from a non-2xx application response. A deterministic failing
// RoundTripper stands in for the failure, so no real socket is dialed.
func TestClientTransportFailureAnswersUnreachable(t *testing.T) {
	hc := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial: connection refused")
	})}
	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL("http://hubspot.invalid"), hubspot.WithHTTPClient(hc))

	_, err := c.List(t.Context(), "contacts", nil, "", 100)
	if err == nil {
		t.Fatal("List: expected a transport-level error, got nil")
	}
	if !errors.Is(err, hubspot.ErrUnreachable) {
		t.Fatalf("List err = %v, want errors.Is(_, ErrUnreachable)", err)
	}
}

// TestClientMalformedSuccessBodyAnswersUnreachable proves a 2xx response
// whose body doesn't decode into the expected shape is a clean
// ErrUnreachable, never a panic on the JSON unmarshal.
func TestClientMalformedSuccessBodyAnswersUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `not-json`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))

	_, err := c.List(t.Context(), "contacts", nil, "", 100)
	if !errors.Is(err, hubspot.ErrUnreachable) {
		t.Fatalf("List err = %v, want errors.Is(_, ErrUnreachable) for an undecodable success body", err)
	}
}

// TestClientWithHTTPClientOverridesTheTransport proves WithHTTPClient
// actually wires the injected client rather than being ignored — a
// custom http.Client with its own Transport must be the one that sends
// the request.
func TestClientWithHTTPClientOverridesTheTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"results":[],"paging":{}}`)
	}))
	defer srv.Close()

	var used bool
	hc := &http.Client{Transport: recordingTransport{inner: http.DefaultTransport, used: &used}}
	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL), hubspot.WithHTTPClient(hc))

	if _, err := c.List(t.Context(), "contacts", nil, "", 100); err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if !used {
		t.Fatal("WithHTTPClient's injected client was never used to send the request")
	}
}

// recordingTransport wraps inner, flagging used when RoundTrip is called
// — the minimal proof WithHTTPClient's http.Client actually sends the
// request, without asserting anything about call ORDER (T11: mock only
// the true boundary, the RoundTripper).
type recordingTransport struct {
	inner http.RoundTripper
	used  *bool
}

func (t recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.used = true
	return t.inner.RoundTrip(r)
}

func TestNewClientRecordsRegion(t *testing.T) {
	c := hubspot.NewClient("eu1", "tok")
	if c.Region() != "eu1" {
		t.Fatalf("Region() = %q, want eu1", c.Region())
	}
	if c.BaseURL() != "https://api.hubapi.com" {
		t.Fatalf("BaseURL() = %q, want https://api.hubapi.com", c.BaseURL())
	}
}

func TestClientAccountIDParsesPortalID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"portalId": 24193752, "timeZone": "US/Eastern"}`)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	id, err := c.AccountID(t.Context())
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	// portalId arrives as a JSON number, returned as its decimal string so it
	// matches the webhook payload's own portalId rendering (the binding key).
	if id != "24193752" {
		t.Errorf("AccountID = %q, want 24193752", id)
	}
	if gotPath != "/account-info/v3/details" {
		t.Errorf("path = %q, want /account-info/v3/details", gotPath)
	}
}

func TestClientAccountIDRejectsMissingPortalID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWrite(t, w, `{"timeZone": "US/Eastern"}`) // no portalId
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	// Fail closed: never bind a webhook to an empty portal.
	if _, err := c.AccountID(t.Context()); err == nil {
		t.Error("a response with no portalId must error, not return an empty portal")
	}
}

func TestClientAccountIDSurfacesUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := hubspot.NewClient("us", "test-token", hubspot.WithBaseURL(srv.URL))
	// An upstream failure must surface (the connect-time fetch treats it as
	// best-effort, but the client itself must not mask it as an empty portal).
	if _, err := c.AccountID(t.Context()); err == nil {
		t.Error("an upstream 500 must surface as an error")
	}
}
