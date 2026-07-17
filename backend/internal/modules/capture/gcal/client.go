// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the Google Calendar provider I/O: the read-only Calendar v3
// REST calls the connector needs, hand-rolled over net/http so capture takes
// on no new module dependency. The OAuth2 handshake is Google's — identical to
// Gmail's — so it is NOT re-implemented here: compose builds the shared Google
// OAuth client (with the calendar-readonly scope) and injects it as the OAuth
// seam. Both OAuth and API are interfaces so the connector's Sync/Authenticate
// are unit tested against a stub, and non-2xx responses map to sentinels the
// transport turns into clean 422/502 without echoing Google's raw text.

package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// httpTimeout bounds every Google call so a stalled Calendar request can't pin
// an API callback or the fleet-wide sync poller (http.DefaultClient has no
// timeout).
const httpTimeout = 30 * time.Second

func boundedHTTPClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// calendarAPIBase is Google's Calendar v3 root; overridable via NewAPI for
// tests. The connector reads the "primary" calendar only.
const calendarAPIBase = "https://www.googleapis.com/calendar/v3"

// initialBackfillDays bounds the first sync's window so a long calendar history
// does not stream unbounded on connect: only events starting within this many
// days back are backfilled, while the returned syncToken still anchors delta
// sync going forward.
const initialBackfillDays = 90

// pageSize bounds one Calendar list page; the connector pages until Google
// returns the nextSyncToken that anchors the next incremental pull.
const pageSize = 250

// qTrue is the string literal Google's events.list expects for its boolean
// query params (singleEvents/showDeleted).
const qTrue = "true"

// ErrAuthRejected marks an OAuth/authorization failure Google reported (revoked
// grant, missing scope). The transport maps it to a 422 without echoing the raw
// provider error.
var ErrAuthRejected = errors.New("gcal: the authorization was rejected")

// ErrUnreachable marks a transport-level failure reaching Google (DNS, TCP,
// TLS, timeout, 5xx). The transport maps it to a 502.
var ErrUnreachable = errors.New("gcal: could not reach Google")

// ErrSyncTokenGone marks a syncToken Google no longer honors (it expires); Sync
// falls back to a bounded re-list rather than failing — the calendar analogue
// of Gmail's ErrHistoryGone.
var ErrSyncTokenGone = errors.New("gcal: sync token expired")

// OAuth is the OAuth2 handshake surface the connector consumes. It is the same
// three-method Google OAuth2 shape the Gmail connector uses; compose builds one
// concrete client per Google scope and injects it here, so this package owns no
// duplicate token plumbing.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (refreshToken string, err error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// API is the read-only Google Calendar surface the connector uses. All calls
// take a short-lived access token (minted from the refresh token per Sync).
type API interface {
	// PrimaryOwner returns the address of the connected user's primary
	// calendar — its id is the owner's email, the internal-vs-external anchor.
	PrimaryOwner(ctx context.Context, accessToken string) (email string, err error)
	// ListInitial returns a bounded backfill of recent events (raw event
	// resources) and the nextSyncToken that anchors delta sync going forward.
	ListInitial(ctx context.Context, accessToken string) (events [][]byte, nextSyncToken string, err error)
	// ListIncremental returns the events changed since syncToken and the
	// advanced token; ErrSyncTokenGone if the token is too old.
	ListIncremental(ctx context.Context, accessToken, syncToken string) (events [][]byte, nextSyncToken string, err error)
}

type httpAPI struct {
	client *http.Client
	base   string
	// now is the clock for the initial-backfill window bound; injectable so a
	// test drives a deterministic timeMin.
	now func() time.Time
}

// NewAPI builds the Calendar REST client over the given HTTP client and base
// URL (default Google when base is empty; tests pass an httptest base).
//
//nolint:ireturn // returns the API seam by design — the connector holds it as an interface so tests substitute a stub
func NewAPI(client *http.Client, base string) API {
	if client == nil {
		client = boundedHTTPClient()
	}
	if base == "" {
		base = calendarAPIBase
	}
	return &httpAPI{client: client, base: base, now: time.Now}
}

func (a *httpAPI) PrimaryOwner(ctx context.Context, accessToken string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if _, err := a.get(ctx, accessToken, "/calendars/primary", nil, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (a *httpAPI) ListInitial(ctx context.Context, accessToken string) ([][]byte, string, error) {
	timeMin := a.now().UTC().AddDate(0, 0, -initialBackfillDays).Format(time.RFC3339)
	q := url.Values{
		"singleEvents": {qTrue}, // expand recurrences → each instance is its own keyable event
		// showDeleted stays true across the whole sync lifecycle: Google rejects
		// showDeleted=false alongside a syncToken, so the initial full sync must
		// use the SAME parameter set the incremental sync will. Cancelled events
		// are dropped by the mapper, not the query.
		"showDeleted": {qTrue},
		"maxResults":  {strconv.Itoa(pageSize)},
		"timeMin":     {timeMin},
	}
	return a.listPages(ctx, accessToken, q)
}

func (a *httpAPI) ListIncremental(ctx context.Context, accessToken, syncToken string) ([][]byte, string, error) {
	q := url.Values{
		"singleEvents": {qTrue}, // must match the initial sync's expansion
		"showDeleted":  {qTrue}, // deletions arrive as status=cancelled — the mapper drops them
		"maxResults":   {strconv.Itoa(pageSize)},
		"syncToken":    {syncToken},
	}
	return a.listPages(ctx, accessToken, q)
}

// eventsPage is one page of calendar.events.list. Items are kept as raw JSON so
// each event's original bytes reach the Sink as evidence unchanged.
type eventsPage struct {
	Items         []json.RawMessage `json:"items"`
	NextPageToken string            `json:"nextPageToken"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	NextSyncToken string            `json:"nextSyncToken"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
}

// listPages walks every page of an events.list query, collecting the raw event
// resources and the terminal nextSyncToken. A 410 on any page (an expired
// syncToken) surfaces as ErrSyncTokenGone so Sync can re-list from scratch.
func (a *httpAPI) listPages(ctx context.Context, accessToken string, base url.Values) ([][]byte, string, error) {
	var events [][]byte
	syncToken := ""
	pageToken := ""
	for {
		q := cloneValues(base)
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page eventsPage
		status, err := a.get(ctx, accessToken, "/calendars/primary/events", q, &page)
		if err != nil {
			if status == http.StatusGone {
				return nil, "", ErrSyncTokenGone
			}
			return nil, "", err
		}
		for _, item := range page.Items {
			events = append(events, []byte(item))
		}
		if page.NextSyncToken != "" {
			syncToken = page.NextSyncToken
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return events, syncToken, nil
}

// get performs an authorized GET and JSON-decodes into out. It returns the HTTP
// status (so listPages can special-case 410) and maps a 401/403 to
// ErrAuthRejected and any other non-2xx/transport failure to ErrUnreachable.
// Google's raw body is never surfaced to the caller.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func (a *httpAPI) get(ctx context.Context, accessToken, path string, q url.Values, out any) (int, error) {
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("gcal: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gcal: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, ErrAuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ErrUnreachable
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("gcal: decoding %s: %w", path, ErrUnreachable)
	}
	return resp.StatusCode, nil
}

// cloneValues copies a url.Values so a per-page pageToken never mutates the
// caller's base query.
func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, v := range in {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
