// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the Google Calendar provider I/O: the read-only Calendar v3
// REST calls the connector needs. The authorized-GET transport and the OAuth2
// handshake are the SHARED Google plumbing (capture/googleconn) — this file
// owns only the Calendar-specific endpoints (primary-calendar owner, the
// events.list sync-token paging) and the one Calendar-specific sentinel
// (ErrSyncTokenGone). Both OAuth and API are interfaces so the connector's
// Sync/Authenticate are unit tested against a stub.

package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
)

// calendarAPIBase is Google's Calendar v3 root; overridable via NewAPI for
// tests. The connector reads the "primary" calendar only.
const calendarAPIBase = "https://www.googleapis.com/calendar/v3"

// Default Google OAuth endpoints; overridable via OAuthConfig for tests. The
// calendar connector authorizes against the same Google identity platform as
// Gmail, but as its OWN authorization (calendar scope only).
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // G101 false positive: Google's public OAuth token *endpoint URL*, not a credential
)

// calendarReadonlyScope is the single Google scope the read-only calendar
// connector requests (event read; no write).
const calendarReadonlyScope = "https://www.googleapis.com/auth/calendar.readonly"

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

// ErrAuthRejected and ErrUnreachable are the shared Google transport sentinels,
// re-exported so this package's callers and tests keep a single gcal-local name.
var (
	ErrAuthRejected = googleconn.ErrAuthRejected
	ErrUnreachable  = googleconn.ErrUnreachable
)

// ErrSyncTokenGone marks a syncToken Google no longer honors (it expires); Sync
// falls back to a bounded re-list rather than failing — the calendar analogue
// of Gmail's ErrHistoryGone.
var ErrSyncTokenGone = errors.New("gcal: sync token expired")

// OAuth is the OAuth2 handshake surface the connector consumes — the shared
// Google shape (googleconn.OAuth). compose builds one concrete client and
// injects it here, so this package owns no duplicate token plumbing.
type OAuth = googleconn.OAuth

// OAuthConfig wires the calendar OAuth client. AuthURL/TokenURL default to
// Google's endpoints when empty; tests override TokenURL.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
}

// NewOAuth builds the calendar OAuth client on the shared oauthflow handshake.
// It is a SEPARATE Google authorization from Gmail's (calendar.readonly only),
// and deliberately omits include_granted_scopes: incremental authorization
// would let a calendar consent granted after a Gmail one silently accrue the
// mail-read scope into this credential, breaking the per-connector scope
// boundary. The gcal error sentinels are supplied so the registry classifies
// failures as calendar's own, with accurate diagnostics.
//
//nolint:ireturn // returns the OAuth seam by design — the connector holds it as an interface so tests substitute a stub
func NewOAuth(cfg OAuthConfig) OAuth {
	if cfg.AuthURL == "" {
		cfg.AuthURL = googleAuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = googleTokenURL
	}
	return oauthflow.New(oauthflow.Config{
		Provider:     connectorName,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       []string{calendarReadonlyScope},
		AuthURL:      cfg.AuthURL,
		TokenURL:     cfg.TokenURL,
		// Google needs offline access + a forced consent prompt to return a
		// refresh token. No include_granted_scopes: keep this credential scoped
		// to the calendar alone.
		AuthParams: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
		},
		AuthRejected: ErrAuthRejected,
		Unreachable:  ErrUnreachable,
	})
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
		client = googleconn.BoundedClient()
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
	if _, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/calendars/primary", nil, &out); err != nil {
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
		status, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/calendars/primary/events", q, &page)
		if err != nil {
			if status == http.StatusGone {
				return nil, "", ErrSyncTokenGone
			}
			return nil, "", err
		}
		for _, item := range page.Items {
			events = append(events, []byte(item))
		}
		if page.NextPageToken == "" {
			// Only the terminal page carries the delta anchor; a nextSyncToken
			// on an earlier (non-terminal) page is not the watermark and must
			// not be trusted if the last page omits it.
			syncToken = page.NextSyncToken
			break
		}
		pageToken = page.NextPageToken
	}
	if syncToken == "" {
		// A fully-walked list must end on a nextSyncToken (Google's delta
		// anchor). Its absence is a provider-contract violation; surface it as
		// retryable rather than persist an empty cursor that would force a full
		// re-backfill every cycle.
		return nil, "", ErrUnreachable
	}
	return events, syncToken, nil
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
