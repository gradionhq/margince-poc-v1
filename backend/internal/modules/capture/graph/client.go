// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the Microsoft Graph provider I/O: the Microsoft identity
// platform OAuth2 handshake and the handful of read-only Graph mail calls the
// connector needs, hand-rolled over net/http so capture takes on no new module
// dependency. Both surfaces are interfaces (OAuth, API) so the connector's
// Sync/Authenticate are unit tested against a stub, and non-2xx responses map
// to sentinels the transport turns into clean 422/502 without echoing
// Microsoft's raw text.

package graph

import (
	"context"
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

// httpTimeout bounds every Microsoft call so a stalled OAuth/Graph request
// can't pin an API callback or the fleet-wide sync poller (http.DefaultClient
// has no timeout).
const httpTimeout = 30 * time.Second

func boundedHTTPClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// Default Microsoft endpoints; overridable via OAuthConfig / NewAPI for tests.
// The identity endpoints are tenant-scoped ("common" serves any work/school or
// personal account); the Graph API base is tenant-less — the access token
// carries the identity.
const (
	msAuthURLFormat  = "https://login.microsoftonline.com/%s/oauth2/v2.0/authorize"
	msTokenURLFormat = "https://login.microsoftonline.com/%s/oauth2/v2.0/token" //nolint:gosec // G101 false positive: Microsoft's public OAuth token *endpoint URL*, not a credential
	graphAPIBase     = "https://graph.microsoft.com/v1.0"

	// defaultTenant is the multi-tenant identity endpoint: any Microsoft 365
	// organization (and personal accounts) can consent. A single-tenant
	// deployment narrows it to its own tenant id via OAuthConfig.Tenant.
	defaultTenant = "common"

	paramClientID = "client_id"
	paramScope    = "scope"
	paramFilter   = "$filter"

	// maxMIMELen bounds one message's fetched RFC822 bytes. A message over
	// this is refused rather than truncated (a truncated MIME blob is not
	// parseable, honest evidence) — the same cap discipline as IMAP.
	maxMIMELen = 8 << 20 // 8 MiB
)

// The package sentinels wrap the shared connector vocabulary (ADR-0063) so
// the registry classifies failures without knowing this provider: auth parks
// the connection, a rate limit honors Retry-After, unreachable backs off.

// ErrAuthRejected marks an OAuth failure Microsoft reported (bad/expired code,
// revoked refresh). The transport maps it to a 422 without echoing the raw
// provider error.
var ErrAuthRejected = fmt.Errorf("graph: the authorization was rejected: %w", connector.ErrAuthRejected)

// ErrUnreachable marks a transport-level failure reaching Microsoft (DNS, TCP,
// TLS, timeout, 5xx). The transport maps it to a 502.
var ErrUnreachable = fmt.Errorf("graph: could not reach Microsoft: %w", connector.ErrUnreachable)

// ErrDeltaGone marks a deltaLink Graph no longer honors (HTTP 410 Gone with a
// resync hint); Sync falls back to a bounded re-anchor rather than failing.
var ErrDeltaGone = fmt.Errorf("graph: delta cursor no longer valid: %w", connector.ErrCursorGone)

// OAuth is the OAuth2 handshake surface: build the consent URL, exchange the
// authorization code for a refresh token, and mint a fresh access token from
// a stored refresh token.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (refreshToken string, err error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// API is the read-only Graph mail surface the connector uses. All calls take
// a short-lived access token (minted from the refresh token per Sync).
type API interface {
	// Profile returns the mailbox owner's address (mail, falling back to
	// userPrincipalName for a mailbox with no mail attribute).
	Profile(ctx context.Context, accessToken string) (email string, err error)
	// DeltaInit starts a fresh inbox delta bounded to messages received on or
	// after the given instant, walks it to completion, and returns the
	// deltaLink the next Delta resumes from.
	DeltaInit(ctx context.Context, accessToken string, after time.Time) (ids []string, deltaLink string, err error)
	// Delta resumes an inbox delta from a stored deltaLink and returns the
	// message ids added/changed since plus the advanced deltaLink;
	// ErrDeltaGone if Graph no longer honors the link.
	Delta(ctx context.Context, accessToken, deltaLink string) (ids []string, newDeltaLink string, err error)
	// GetMIME fetches one message as its RFC822 bytes (the /$value stream).
	GetMIME(ctx context.Context, accessToken, msgID string) (rfc822 []byte, err error)
	// EstimateAfter returns the provider-side count of messages received on
	// or after the given instant ($count=true) — the backfill preview's number.
	EstimateAfter(ctx context.Context, accessToken string, after time.Time) (int, error)
	// ListAfter returns one page of message ids received on or after the
	// given instant; pageToken is the @odata.nextLink of the prior page
	// ("" starts the walk).
	ListAfter(ctx context.Context, accessToken string, after time.Time, pageToken string, pageSize int) (ids []string, next string, err error)
}

// OAuthConfig wires the OAuth client. Tenant defaults to "common";
// AuthURL/TokenURL default to Microsoft's tenant-scoped endpoints when empty
// (tests override TokenURL).
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Tenant       string
	Scopes       []string
	AuthURL      string
	TokenURL     string
}

// NewOAuth builds the OAuth client, applying Microsoft's default endpoints
// (for the configured tenant) when the config leaves them empty. The
// handshake itself is the shared oauthflow; only Microsoft's tenant-scoped
// endpoints, consent parameters, and this package's sentinels differ.
//
//nolint:ireturn // returns the OAuth seam by design — the connector holds it as an interface so tests substitute a stub
func NewOAuth(cfg OAuthConfig) OAuth {
	tenant := cfg.Tenant
	if tenant == "" {
		tenant = defaultTenant
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = fmt.Sprintf(msAuthURLFormat, url.PathEscape(tenant))
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = fmt.Sprintf(msTokenURLFormat, url.PathEscape(tenant))
	}
	return oauthflow.New(oauthflow.Config{
		Provider:     "graph",
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       cfg.Scopes,
		AuthURL:      cfg.AuthURL,
		TokenURL:     cfg.TokenURL,
		// Microsoft returns the code on the query (not fragment); the v2
		// endpoint requires the scope in every token form for a refresh token.
		// Microsoft rotates the refresh token on each redemption. The stored
		// original keeps working within its lifetime (a confidential-client
		// refresh token is valid ~90 days), so an actively-synced mailbox
		// never reauths; a mailbox idle past that window must reconnect. A
		// credential-rotation seam (Sync surfacing an updated credential for
		// the registry to re-seal) is a tracked follow-up, not this PR.
		AuthParams:        map[string]string{"response_mode": "query"},
		ScopeInTokenForms: true,
		AuthRejected:      ErrAuthRejected,
		Unreachable:       ErrUnreachable,
	})
}

type httpAPI struct {
	client *http.Client
	base   string
}

// NewAPI builds the Graph REST client over the given HTTP client and base
// URL (default Microsoft when base is empty; tests pass an httptest base).
//
//nolint:ireturn // returns the API seam by design — the connector holds it as an interface so tests substitute a stub
func NewAPI(client *http.Client, base string) API {
	if client == nil {
		client = boundedHTTPClient()
	}
	if base == "" {
		base = graphAPIBase
	}
	return &httpAPI{client: client, base: base}
}

func (a *httpAPI) Profile(ctx context.Context, accessToken string) (string, error) {
	var out struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"` //nolint:tagliatelle // Microsoft's wire format (camelCase); must match to decode
	}
	if _, err := a.get(ctx, accessToken, a.base+"/me", nil, &out); err != nil {
		return "", err
	}
	if out.Mail != "" {
		return out.Mail, nil
	}
	return out.UserPrincipalName, nil
}

// receivedAfterFilter renders Graph's only supported delta/list filter:
// receivedDateTime ge <instant>. The window boundary is a product parameter
// measured in months, so second-grain UTC rendering loses nothing.
func receivedAfterFilter(after time.Time) string {
	return "receivedDateTime ge " + after.UTC().Format(time.RFC3339)
}

// deltaPage is one page of the inbox messages delta. A tombstoned entry
// carries @removed instead of message fields — nothing to fetch for it.
type deltaPage struct {
	Value []struct {
		ID      string           `json:"id"`
		Removed *json.RawMessage `json:"@removed"`
	} `json:"value"`
	NextLink  string `json:"@odata.nextLink"`  //nolint:tagliatelle // Microsoft's wire format; must match to decode
	DeltaLink string `json:"@odata.deltaLink"` //nolint:tagliatelle // Microsoft's wire format; must match to decode
}

func (a *httpAPI) DeltaInit(ctx context.Context, accessToken string, after time.Time) ([]string, string, error) {
	q := url.Values{paramFilter: {receivedAfterFilter(after)}}
	return a.deltaWalk(ctx, accessToken, a.base+"/me/mailFolders/inbox/messages/delta?"+q.Encode())
}

func (a *httpAPI) Delta(ctx context.Context, accessToken, deltaLink string) ([]string, string, error) {
	if err := a.sameAPIOrigin(deltaLink); err != nil {
		return nil, "", err
	}
	return a.deltaWalk(ctx, accessToken, deltaLink)
}

// deltaWalk follows a delta round from startURL through every nextLink until
// Graph hands back the deltaLink that closes it, collecting the ids of the
// non-tombstoned messages along the way. A 410 anywhere in the walk means the
// server no longer honors this delta state (ErrDeltaGone).
func (a *httpAPI) deltaWalk(ctx context.Context, accessToken, startURL string) ([]string, string, error) {
	var ids []string
	next := startURL
	for {
		var page deltaPage
		status, err := a.get(ctx, accessToken, next, nil, &page)
		if err != nil {
			if status == http.StatusGone {
				return nil, "", ErrDeltaGone
			}
			return nil, "", err
		}
		for _, m := range page.Value {
			if m.ID != "" && m.Removed == nil {
				ids = append(ids, m.ID)
			}
		}
		if page.NextLink == "" {
			return ids, page.DeltaLink, nil
		}
		if err := a.sameAPIOrigin(page.NextLink); err != nil {
			return nil, "", err
		}
		next = page.NextLink
	}
}

// sameAPIOrigin refuses any continuation URL that does not live under the
// configured Graph base. nextLink/deltaLink are server-supplied URLs the
// client follows bearing the access token — and the deltaLink round-trips
// through the stored sync cursor — so an off-origin link (tampered cursor,
// broken provider) must never be fetched.
func (a *httpAPI) sameAPIOrigin(link string) error {
	if !strings.HasPrefix(link, a.base+"/") && link != a.base {
		return fmt.Errorf("graph: continuation link does not point at the graph api")
	}
	return nil
}

func (a *httpAPI) GetMIME(ctx context.Context, accessToken, msgID string) ([]byte, error) {
	u := a.base + "/me/messages/" + url.PathEscape(msgID) + "/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("graph: building message request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph: message %s: %w", msgID, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the message body — the MIME bytes are already read below
	defer func() { _ = resp.Body.Close() }()
	// Read one byte past the cap so an oversized message is detected, not
	// silently truncated: LimitReader alone returns EOF exactly at the cap,
	// which would parse and persist a body missing its tail (the same
	// discipline the IMAP connector's readCapped uses).
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMIMELen+1))
	if err != nil {
		return nil, fmt.Errorf("graph: reading message %s: %w", msgID, ErrUnreachable)
	}
	if err := classifyStatus(resp); err != nil {
		return nil, err
	}
	if len(body) > maxMIMELen {
		// Oversized: a truncated MIME blob is neither parseable nor honest
		// evidence, so it is refused (counted as a skip upstream), never
		// stored half-read.
		return nil, fmt.Errorf("graph: message %s exceeds the size cap: %w", msgID, connector.ErrSkip)
	}
	return body, nil
}

func (a *httpAPI) EstimateAfter(ctx context.Context, accessToken string, after time.Time) (int, error) {
	var out struct {
		Count int `json:"@odata.count"` //nolint:tagliatelle // Microsoft's wire format; must match to decode
	}
	q := url.Values{
		paramFilter: {receivedAfterFilter(after)},
		"$count":    {"true"},
		"$select":   {"id"},
		"$top":      {"1"},
	}
	// $count=true needs the eventual-consistency header on Graph.
	hdr := http.Header{"ConsistencyLevel": {"eventual"}}
	if _, err := a.get(ctx, accessToken, a.base+"/me/messages?"+q.Encode(), hdr, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (a *httpAPI) ListAfter(ctx context.Context, accessToken string, after time.Time, pageToken string, pageSize int) ([]string, string, error) {
	u := pageToken
	if u == "" {
		q := url.Values{
			paramFilter: {receivedAfterFilter(after)},
			"$select":   {"id"},
			"$top":      {strconv.Itoa(pageSize)},
		}
		u = a.base + "/me/messages?" + q.Encode()
	} else if err := a.sameAPIOrigin(u); err != nil {
		return nil, "", err
	}
	var out struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
		NextLink string `json:"@odata.nextLink"` //nolint:tagliatelle // Microsoft's wire format; must match to decode
	}
	if _, err := a.get(ctx, accessToken, u, nil, &out); err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(out.Value))
	for _, m := range out.Value {
		ids = append(ids, m.ID)
	}
	return ids, out.NextLink, nil
}

// retryAfter parses the provider's Retry-After (delta-seconds form; Graph's
// throttling responses use it). Zero when absent — the caller's own backoff
// takes over.
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// classifyStatus maps a non-2xx Graph response onto the shared connector
// vocabulary: 429 honors Retry-After, 401/403 parks the credential, anything
// else backs off. Microsoft's raw body is never surfaced to the caller.
func classifyStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuthRejected
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return ErrUnreachable
	}
	return nil
}

// get performs an authorized GET on a full URL (extra headers optional) and
// JSON-decodes into out. It returns the HTTP status (so deltaWalk can
// special-case 410) alongside the classified error.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func (a *httpAPI) get(ctx context.Context, accessToken, fullURL string, hdr http.Header, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, fmt.Errorf("graph: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("graph: request: %w", ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// Classify on status/headers first: a 429/401 must be honored even if the
	// body read failed. Only on an otherwise-OK response does a read failure
	// matter — a truncated-but-valid-JSON prefix must never pass as complete.
	if err := classifyStatus(resp); err != nil {
		return resp.StatusCode, err
	}
	if readErr != nil {
		return resp.StatusCode, fmt.Errorf("graph: reading response: %w", ErrUnreachable)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("graph: decoding response: %w", ErrUnreachable)
	}
	return resp.StatusCode, nil
}
