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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
)

// Default Google endpoints; overridable via OAuthConfig / NewAPI for tests.
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // G101 false positive: Google's public OAuth token *endpoint URL*, not a credential
	gmailAPIBase   = "https://gmail.googleapis.com/gmail/v1/users/me"

	paramClientID = "client_id"
)

// ErrAuthRejected and ErrUnreachable are the shared Google transport sentinels,
// re-exported so this package's callers and tests keep a single gmail-local name.
var (
	ErrAuthRejected = googleconn.ErrAuthRejected
	ErrUnreachable  = googleconn.ErrUnreachable
)

// ErrHistoryGone marks a startHistoryId Gmail no longer has (it expires
// after ~a week); Sync falls back to a bounded re-list rather than failing.
var ErrHistoryGone = errors.New("gmail: history cursor too old")

// OAuth is the OAuth2 handshake surface — the shared Google shape
// (googleconn.OAuth): build the consent URL, exchange the authorization code for
// a refresh token, and mint a fresh access token from a stored refresh token.
type OAuth = googleconn.OAuth

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
	return &httpOAuth{client: googleconn.BoundedClient(), cfg: cfg}
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
		client = googleconn.BoundedClient()
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
	if _, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/profile", nil, &out); err != nil {
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
	if _, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/messages", q, &out); err != nil {
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
		status, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/history", q, &page)
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
	if _, err := googleconn.Get(ctx, a.client, a.base, accessToken, "/messages/"+url.PathEscape(msgID), q, &out); err != nil {
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
