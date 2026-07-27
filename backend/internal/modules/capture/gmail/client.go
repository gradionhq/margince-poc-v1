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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// watchOp names the users.watch call in a ProviderError. The other Gmail calls
// carry their API path as the op; watch is a POST built by hand, so it names
// itself.
const watchOp = "watch"

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

// ErrMessageGone marks a message id that Gmail 404s on GetRaw — deleted or
// moved between enumeration and fetch, a routine event across a months-long
// backfill. It wraps connector.ErrSkip so the fetch loops treat one vanished
// message as a skip and keep going: a single gone id must never abort the
// whole pull and wedge the mailbox behind the backoff ladder. There is
// nothing to capture — the message no longer exists.
var ErrMessageGone = fmt.Errorf("gmail: message no longer exists: %w", connector.ErrSkip)

// OAuth is the OAuth2 handshake surface: build the consent URL, exchange the
// authorization code for a refresh token, and mint a fresh access token from
// a stored refresh token.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (oauthflow.TokenGrant, error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// sentLabelID is Gmail's system label for the mailbox owner's own sent mail.
// System label ids are stable strings, not localized names.
const sentLabelID = "SENT"

// Message is one fetched Gmail message: the decoded RFC822 bytes plus the one
// thing the bytes cannot honestly tell us — whether Gmail itself filed the
// message under SENT (ADR-0072 §1's provider evidence). Both come off the same
// messages.get response, so the signal costs no extra call.
type Message struct {
	RFC822 []byte
	// FiledAsSent is the provider half of the T1 evidence and nothing more:
	// Gmail labelled this message SENT. On its own it does not mean the owner
	// wrote it — mailmap composes it with the message's authorship before any
	// attestation is claimed.
	FiledAsSent bool
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
	// GetRaw fetches one message as its decoded RFC822 bytes (format=RAW)
	// together with Gmail's own SENT filing of it, read off the same response.
	GetRaw(ctx context.Context, accessToken, msgID string) (Message, error)
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
	if _, err := a.get(ctx, accessToken, "/profile", nil, &out, maxJSONResponseBytes); err != nil {
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
	if _, err := a.get(ctx, accessToken, "/messages", q, &out, maxJSONResponseBytes); err != nil {
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
		status, err := a.get(ctx, accessToken, "/history", q, &page, maxJSONResponseBytes)
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

func (a *httpAPI) GetRaw(ctx context.Context, accessToken, msgID string) (Message, error) {
	var out struct {
		Raw      string   `json:"raw"`
		LabelIDs []string `json:"labelIds"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	}
	q := url.Values{"format": {"RAW"}}
	status, err := a.get(ctx, accessToken, "/messages/"+url.PathEscape(msgID), q, &out, maxRawMessageBytes)
	if status == http.StatusNotFound {
		// Enumerated a moment ago, gone now — nothing to fetch. Skip it and
		// let the pull continue rather than abort on a message that no longer
		// exists (get maps every non-200 to ErrUnreachable; the 404 is not a
		// reachability problem).
		return Message{}, ErrMessageGone
	}
	if err != nil {
		return Message{}, err
	}
	// Gmail encodes the RFC822 as web-safe (URL) base64, padding-optional.
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(out.Raw, "="))
	if err != nil {
		return Message{}, fmt.Errorf("gmail: decoding raw message %s: %w", msgID, ErrUnreachable)
	}
	return Message{RFC822: decoded, FiledAsSent: hasSentLabel(out.LabelIDs)}, nil
}

// hasSentLabel reports whether Gmail filed this message under SENT — the
// provider's own attestation that the authenticated mailbox owner sent it.
// Gmail sets the label on delivery to the sender's own mailbox; a message that
// merely claims the owner in its From header never carries it, which is exactly
// why the T1 correspondence gate (ADR-0072 §1) reads this and not the header.
func hasSentLabel(labelIDs []string) bool {
	return slices.Contains(labelIDs, sentLabelID)
}

// Watch registers a users.watch so Gmail publishes change notifications for
// the mailbox to the Pub/Sub topic. Gmail returns the mailbox's current
// historyId and an expiration as a string of milliseconds since the epoch;
// re-calling watch renews it (Gmail keeps one watch per mailbox). A non-200 is
// classified by classifyStatus like every other Gmail call — Google's raw body
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
	if err := classifyStatus(resp, watchOp, body); err != nil {
		return "", time.Time{}, err
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

// classifyStatus is the ONE status verdict for every Gmail call: throttling
// first (a 429, or a 403 whose reason names a limit) so a paced mailbox backs off
// with the provider's own Retry-After; then a refused credential; then anything
// else non-OK. It returns nil for a 200. Every call goes through it so the ladder
// cannot fork per call site and disagree about what the same 403 means — which it
// did, leaving one caller unable to see a rate limit at all and discarding the
// Retry-After Google had supplied.
func classifyStatus(resp *http.Response, op string, body []byte) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	case resp.StatusCode == http.StatusForbidden && googleconn.RateLimitBody(body):
		return &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &connector.ProviderError{
			Op: op, Status: resp.StatusCode, Reason: googleconn.Reason(body), Class: ErrAuthRejected,
		}
	case resp.StatusCode != http.StatusOK:
		return &connector.ProviderError{
			Op: op, Status: resp.StatusCode, Reason: googleconn.Reason(body), Class: ErrUnreachable,
		}
	}
	return nil
}

// retryAfter parses the provider's Retry-After (delta-seconds form; Google
// does not send HTTP-dates here). Zero when absent — the caller's own backoff
// takes over.
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// Read caps for the Gmail JSON responses. The metadata calls (profile, id
// lists, history deltas) are a few KB; a single messages.get?format=RAW is
// the whole RFC822 message base64url-encoded inside JSON. Gmail (Workspace)
// receives messages up to ~50 MiB, and base64 inflates ~1.37×, so a RAW
// response can exceed 68 MiB — the cap has to clear that or a large but
// ordinary email (a phone photo) truncates the body, the JSON decode fails,
// and the whole pull aborts on ErrUnreachable, wedging the mailbox. 96 MiB
// leaves headroom above the provider ceiling; the metadata cap stays tight.
const (
	maxJSONResponseBytes = 8 << 20  // 8 MiB — metadata responses
	maxRawMessageBytes   = 96 << 20 // 96 MiB — a full-size RAW message
)

// get performs an authorized GET and JSON-decodes into out. It returns the
// HTTP status (so History can special-case 404) alongside the failure
// classifyStatus assigns, each carrying Google's own reason code. Google's raw
// body is never surfaced to the caller.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func (a *httpAPI) get(ctx context.Context, accessToken, path string, q url.Values, out any, maxBytes int64) (int, error) {
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
	// A read fault mid-body is a real reachability failure, distinct from the
	// size cap (LimitReader signals the cap with EOF, not an error). Surface it
	// as such rather than letting a truncated body fail the decode with a
	// misleading "decoding" error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("gmail: reading %s: %w", path, ErrUnreachable)
	}
	if err := classifyStatus(resp, path, body); err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("gmail: decoding %s: %w", path, ErrUnreachable)
	}
	return resp.StatusCode, nil
}
