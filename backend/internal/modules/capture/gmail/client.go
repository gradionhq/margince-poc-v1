// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the Gmail provider I/O: the OAuth2 handshake and the handful
// of read-only Gmail REST calls the connector needs, hand-rolled over
// net/http so capture takes on no new module dependency. Both surfaces are
// interfaces (OAuth, API) so the connector's Sync/Authenticate are unit
// tested against a stub, and non-2xx responses map to sentinels the
// transport turns into clean 422/502 without echoing Google's raw text.

package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// httpTimeout bounds every Google call so a stalled OAuth/Gmail request can't
// pin an API callback or the fleet-wide sync poller (http.DefaultClient has no
// timeout).
const httpTimeout = 30 * time.Second

func boundedHTTPClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// Default Google endpoints; overridable via OAuthConfig / NewAPI for tests.
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // G101 false positive: Google's public OAuth token *endpoint URL*, not a credential
	gmailAPIBase   = "https://gmail.googleapis.com/gmail/v1/users/me"
)

// The package sentinels wrap the shared connector vocabulary (ADR-0063) so
// the registry classifies failures without knowing this provider: auth parks
// the connection, a rate limit honors Retry-After, unreachable backs off.

// ErrAuthRejected marks an OAuth failure Google reported (bad/expired code,
// revoked refresh). The transport maps it to a 422 without echoing the raw
// provider error.
var ErrAuthRejected = fmt.Errorf("gmail: the authorization was rejected: %w", connector.ErrAuthRejected)

// ErrUnreachable marks a transport-level failure reaching Google (DNS, TCP,
// TLS, timeout, 5xx). The transport maps it to a 502.
var ErrUnreachable = fmt.Errorf("gmail: could not reach Google: %w", connector.ErrUnreachable)

// ErrHistoryGone marks a startHistoryId Gmail no longer has (it expires
// after ~a week); Sync falls back to a bounded re-list rather than failing.
var ErrHistoryGone = fmt.Errorf("gmail: history cursor too old: %w", connector.ErrCursorGone)

// OAuth is the OAuth2 handshake surface: build the consent URL, exchange the
// authorization code for a refresh token, and mint a fresh access token from
// a stored refresh token.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (refreshToken string, err error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// API is the read-only Gmail surface the connector uses. All calls take a
// short-lived access token (minted from the refresh token per Sync).
type API interface {
	// Profile returns the mailbox owner's address and the mailbox's current
	// historyId — the anchor a first sync stores as its cursor.
	Profile(ctx context.Context, accessToken string) (email, historyID string, err error)
	// ListRecent returns the ids of the most recent messages, bounded by maxResults.
	ListRecent(ctx context.Context, accessToken string, maxResults int) (ids []string, err error)
	// History returns the message ids added since startHistoryID and the
	// advanced historyId; ErrHistoryGone if the cursor is too old.
	History(ctx context.Context, accessToken, startHistoryID string) (addedIDs []string, historyID string, err error)
	// GetRaw fetches one message as its decoded RFC822 bytes (format=RAW).
	GetRaw(ctx context.Context, accessToken, msgID string) (rfc822 []byte, err error)
	// EstimateAfter returns the provider-side message count for a query
	// (resultSizeEstimate) — the backfill preview's number.
	EstimateAfter(ctx context.Context, accessToken, query string) (int, error)
	// ListAfter returns one page of message ids matching query.
	ListAfter(ctx context.Context, accessToken, query, pageToken string, pageSize int) (ids []string, next string, err error)
	// Watch registers (or renews) a users.watch against the given Pub/Sub
	// topic and returns the mailbox's historyId at watch time plus the watch's
	// expiration (Gmail caps a watch at 7 days).
	Watch(ctx context.Context, accessToken, topic string) (historyID string, expiration time.Time, err error)
}

// OAuthConfig wires the OAuth client. AuthURL/TokenURL default to Google's
// endpoints when empty; tests override TokenURL.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	AuthURL      string
	TokenURL     string
}

// NewOAuth builds the OAuth client, applying Google's default endpoints when
// the config leaves them empty. The handshake itself is the shared
// oauthflow; only Google's endpoints, consent parameters, and this package's
// sentinels are supplied here.
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
		Provider:     "gmail",
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       cfg.Scopes,
		AuthURL:      cfg.AuthURL,
		TokenURL:     cfg.TokenURL,
		// Google needs offline access + a forced consent prompt to return a
		// refresh token. No include_granted_scopes: with a second read-only
		// connector (gcal) on the same Google app, incremental authorization
		// would let whichever mailbox/calendar is connected second accrete the
		// other's scope into its credential — keep each grant to its own scope.
		AuthParams: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
		},
		AuthRejected: ErrAuthRejected,
		Unreachable:  ErrUnreachable,
	})
}

type httpAPI struct {
	client *http.Client
	base   string
}

// NewAPI builds the Gmail REST client over the given HTTP client and base
// URL (default Google when base is empty; tests pass an httptest base).
//
//nolint:ireturn // returns the API seam by design — the connector holds it as an interface so tests substitute a stub
func NewAPI(client *http.Client, base string) API {
	if client == nil {
		client = boundedHTTPClient()
	}
	if base == "" {
		base = gmailAPIBase
	}
	return &httpAPI{client: client, base: base}
}

func (a *httpAPI) Profile(ctx context.Context, accessToken string) (string, string, error) {
	var out struct {
		EmailAddress string `json:"emailAddress"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
		HistoryID    string `json:"historyId"`    //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	}
	if _, err := a.get(ctx, accessToken, "/profile", nil, &out); err != nil {
		return "", "", err
	}
	return out.EmailAddress, out.HistoryID, nil
}

func (a *httpAPI) ListRecent(ctx context.Context, accessToken string, maxResults int) ([]string, error) {
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{"maxResults": {strconv.Itoa(maxResults)}}
	if _, err := a.get(ctx, accessToken, "/messages", q, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// historyPage is one page of users.history.list.
type historyPage struct {
	History []struct {
		MessagesAdded []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"messagesAdded"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	} `json:"history"`
	HistoryID     string `json:"historyId"`     //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	NextPageToken string `json:"nextPageToken"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
}

func (a *httpAPI) History(ctx context.Context, accessToken, startHistoryID string) ([]string, string, error) {
	var ids []string
	latest := startHistoryID
	pageToken := ""
	for {
		q := url.Values{
			"startHistoryId": {startHistoryID},
			"historyTypes":   {"messageAdded"},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page historyPage
		status, err := a.get(ctx, accessToken, "/history", q, &page)
		if err != nil {
			if status == http.StatusNotFound {
				return nil, "", ErrHistoryGone
			}
			return nil, "", err
		}
		for _, h := range page.History {
			for _, ma := range h.MessagesAdded {
				if ma.Message.ID != "" {
					ids = append(ids, ma.Message.ID)
				}
			}
		}
		if page.HistoryID != "" {
			latest = page.HistoryID
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return ids, latest, nil
}

func (a *httpAPI) GetRaw(ctx context.Context, accessToken, msgID string) ([]byte, error) {
	var out struct {
		Raw string `json:"raw"`
	}
	q := url.Values{"format": {"RAW"}}
	if _, err := a.get(ctx, accessToken, "/messages/"+url.PathEscape(msgID), q, &out); err != nil {
		return nil, err
	}
	// Gmail encodes the RFC822 as web-safe (URL) base64, padding-optional.
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(out.Raw, "="))
	if err != nil {
		return nil, fmt.Errorf("gmail: decoding raw message %s: %w", msgID, ErrUnreachable)
	}
	return decoded, nil
}

// Watch registers a users.watch so Gmail publishes change notifications for
// the mailbox to the Pub/Sub topic. Gmail returns the mailbox's current
// historyId and an expiration as a string of milliseconds since the epoch;
// re-calling watch renews it (Gmail keeps one watch per mailbox). A 401/403
// maps to ErrAuthRejected, anything else to ErrUnreachable — Google's raw body
// never reaches the caller.
func (a *httpAPI) Watch(ctx context.Context, accessToken, topic string) (string, time.Time, error) {
	reqBody, err := json.Marshal(map[string]string{"topicName": topic})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gmail: encoding watch request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/watch", bytes.NewReader(reqBody))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gmail: building watch request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gmail: watch: %w", ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the watch response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", time.Time{}, ErrAuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, ErrUnreachable
	}
	var out struct {
		HistoryID  string `json:"historyId"`  //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
		Expiration string `json:"expiration"` // ms since epoch, as a string
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("gmail: decoding watch response: %w", ErrUnreachable)
	}
	ms, err := strconv.ParseInt(out.Expiration, 10, 64)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gmail: unparseable watch expiration %q: %w", out.Expiration, ErrUnreachable)
	}
	return out.HistoryID, time.UnixMilli(ms), nil
}

// get performs an authorized GET and JSON-decodes into out. It returns the
// HTTP status (so History can special-case 404) and maps a 401/403 to
// ErrAuthRejected and any other non-2xx/transport failure to ErrUnreachable.
// Google's raw body is never surfaced to the caller.
//
// retryAfter parses the provider's Retry-After (delta-seconds form; Google
// does not send HTTP-dates here). Zero when absent — the caller's own backoff
// takes over.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func (a *httpAPI) get(ctx context.Context, accessToken, path string, q url.Values, out any) (int, error) {
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("gmail: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gmail: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	}
	if resp.StatusCode == http.StatusForbidden && bytes.Contains(body, []byte("ateLimitExceeded")) {
		// Google reports per-user quota as 403 with reason rateLimitExceeded /
		// userRateLimitExceeded — a pacing problem, not an authorization one.
		return resp.StatusCode, &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, ErrAuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ErrUnreachable
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("gmail: decoding %s: %w", path, ErrUnreachable)
	}
	return resp.StatusCode, nil
}
