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
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

	paramClientID = "client_id"
)

// ErrAuthRejected marks an OAuth failure Google reported (bad/expired code,
// revoked refresh). The transport maps it to a 422 without echoing the raw
// provider error.
var ErrAuthRejected = errors.New("gmail: the authorization was rejected")

// ErrUnreachable marks a transport-level failure reaching Google (DNS, TCP,
// TLS, timeout, 5xx). The transport maps it to a 502.
var ErrUnreachable = errors.New("gmail: could not reach Google")

// ErrHistoryGone marks a startHistoryId Gmail no longer has (it expires
// after ~a week); Sync falls back to a bounded re-list rather than failing.
var ErrHistoryGone = errors.New("gmail: history cursor too old")

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

type httpOAuth struct {
	client *http.Client
	cfg    OAuthConfig
}

// NewOAuth builds the OAuth client, applying Google's default endpoints when
// the config leaves them empty.
//
//nolint:ireturn // returns the OAuth seam by design — the connector holds it as an interface so tests substitute a stub
func NewOAuth(cfg OAuthConfig) OAuth {
	if cfg.AuthURL == "" {
		cfg.AuthURL = googleAuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = googleTokenURL
	}
	return &httpOAuth{client: boundedHTTPClient(), cfg: cfg}
}

func (o *httpOAuth) AuthCodeURL(state, redirectURI string) string {
	q := url.Values{
		paramClientID:            {o.cfg.ClientID},
		"redirect_uri":           {redirectURI},
		"response_type":          {"code"},
		"scope":                  {strings.Join(o.cfg.Scopes, " ")},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"state":                  {state},
	}
	return o.cfg.AuthURL + "?" + q.Encode()
}

// tokenResponse is the subset of Google's token endpoint payload we read.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (o *httpOAuth) Exchange(ctx context.Context, code, redirectURI string) (string, error) {
	tok, err := o.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		paramClientID:   {o.cfg.ClientID},
		"client_secret": {o.cfg.ClientSecret},
	})
	if err != nil {
		return "", err
	}
	if tok.RefreshToken == "" {
		// No refresh token means the consent did not grant offline access —
		// we cannot sync later, so treat it as a rejected authorization.
		return "", fmt.Errorf("gmail: consent returned no refresh token: %w", ErrAuthRejected)
	}
	return tok.RefreshToken, nil
}

func (o *httpOAuth) AccessToken(ctx context.Context, refreshToken string) (string, error) {
	tok, err := o.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		paramClientID:   {o.cfg.ClientID},
		"client_secret": {o.cfg.ClientSecret},
	})
	if err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("gmail: token refresh returned no access token: %w", ErrAuthRejected)
	}
	return tok.AccessToken, nil
}

// token posts the form to the token endpoint and decodes the response. A 4xx
// is an authorization problem (ErrAuthRejected); anything else reaching or
// reading Google is ErrUnreachable. Google's raw body never reaches the caller.
func (o *httpOAuth) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("gmail: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("gmail: token endpoint: %w", ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the token response body — the exchange result is already read below
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return tokenResponse{}, ErrAuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, ErrUnreachable
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("gmail: decoding token response: %w", ErrUnreachable)
	}
	return tok, nil
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

// get performs an authorized GET and JSON-decodes into out. It returns the
// HTTP status (so History can special-case 404) and maps a 401/403 to
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
