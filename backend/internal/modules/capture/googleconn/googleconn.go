// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package googleconn is the shared plumbing for the capture connectors that
// talk to Google over OAuth2 + REST (gmail, gcal): the authorized read-only
// GET with sentinel error mapping, and the OAuth code→refresh→access→owner
// Authenticate handshake with its persisted auth state. It is the Google
// analogue of capture/mailmap — extracted once the second concrete caller
// (gcal) appeared (ADR-0054 §3: grow a shared subpackage when a real second
// caller shows up, not for symmetry). It owns no provider specifics: each
// connector keeps the API surface, cursor shape, and extra error sentinels
// particular to it (Gmail's historyId / ErrHistoryGone, Calendar's syncToken /
// ErrSyncTokenGone).
package googleconn

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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// httpTimeout bounds every Google call so a stalled request can't pin an API
// callback or the fleet-wide sync poller (http.DefaultClient has no timeout).
const httpTimeout = 30 * time.Second

// BoundedClient returns an HTTP client with the standard Google-call timeout.
func BoundedClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// The package sentinels wrap the shared connector vocabulary (ADR-0063) so the
// registry classifies a failure without knowing the provider: a rejected auth
// parks the connection, a rate limit honors Retry-After, and an unreachable
// provider backs off (rather than every failure becoming a terminal error).

// ErrAuthRejected marks an OAuth/authorization failure Google reported (bad or
// expired code, revoked grant, missing scope). The transport maps it to a 422
// without echoing the raw provider error.
var ErrAuthRejected = fmt.Errorf("googleconn: the authorization was rejected: %w", connector.ErrAuthRejected)

// ErrUnreachable marks a transport-level failure reaching Google (DNS, TCP,
// TLS, timeout, 5xx, or a truncated/undecodable body). The transport maps it to
// a 502 and the registry retries with backoff.
var ErrUnreachable = fmt.Errorf("googleconn: could not reach Google: %w", connector.ErrUnreachable)

// Get performs an authorized GET against base+path and JSON-decodes the 200
// body into out. It returns the HTTP status (so a caller can special-case a
// provider code like 404/410) alongside the classified failure: a 429 or a
// quota-reason 403 becomes a RateLimitedError carrying Retry-After, a 401/403
// ErrAuthRejected, and any other non-2xx or transport fault ErrUnreachable —
// each carrying the path and Google's own machine reason so the failure is
// diagnosable. Google's raw body is never surfaced to the caller.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func Get(ctx context.Context, client *http.Client, base, accessToken, path string, q url.Values, out any) (int, error) {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("googleconn: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("googleconn: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// A throttled provider is weather, not a bad credential: honor Retry-After
	// and let the registry back off rather than parking the connection. Google
	// signals quota/rate limits as 429, or as 403 with a rate/quota reason.
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	}
	if resp.StatusCode == http.StatusForbidden && RateLimitBody(body) {
		return resp.StatusCode, &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, &connector.ProviderError{
			Op: path, Status: resp.StatusCode, Reason: Reason(body), Class: ErrAuthRejected,
		}
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, &connector.ProviderError{
			Op: path, Status: resp.StatusCode, Reason: Reason(body), Class: ErrUnreachable,
		}
	}
	if readErr != nil {
		// A truncated body that happens to be a valid-JSON prefix must never
		// pass as a complete response — treat the read failure as unreachable.
		return resp.StatusCode, fmt.Errorf("googleconn: reading %s: %w", path, ErrUnreachable)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("googleconn: decoding %s: %w", path, ErrUnreachable)
	}
	return resp.StatusCode, nil
}

// The Google reason codes that mean THE DEPLOYMENT IS NOT CONFIGURED, not that
// a credential went bad: the API this connector calls was never enabled for the
// Google Cloud project behind the OAuth client. Google reports it as a 403 —
// indistinguishable, by status alone, from a revoked grant — so without the
// reason code the failure reads as "reconnect to resume", advice that can never
// work: only an administrator enabling the API in the project fixes it.
// accessNotConfigured is the classic reason; SERVICE_DISABLED is the same fact
// in Google's newer ErrorInfo details.
const (
	reasonAccessNotConfigured = "accessNotConfigured"
	reasonServiceDisabled     = "SERVICE_DISABLED"
)

// Misconfigured reports whether err is a Google refusal that names a disabled
// API rather than a bad credential — the one auth-rejected case a human cannot
// fix by reconnecting.
func Misconfigured(err error) bool {
	switch connector.ProviderReason(err) {
	case reasonAccessNotConfigured, reasonServiceDisabled:
		return true
	default:
		return false
	}
}

// googleErrorBody is the subset of Google's standard error envelope that names
// the failure. errors[].reason is the classic form; details[].reason is the
// google.rpc.ErrorInfo form the newer APIs return; status is the enum both
// carry. Only these fixed machine codes are read — the prose message stays in
// the body and never travels.
type googleErrorBody struct {
	Error struct {
		Status string `json:"status"`
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

// reasonCodes separates the SPECIFIC reason codes (the classic errors[] entries,
// then the ErrorInfo details[]) from the overall status, so each caller states
// its own use for the status rather than inheriting one from the order of a
// single list. Values are raw — validation is the caller's business.
func reasonCodes(body []byte) (codes []string, status string) {
	var parsed googleErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, ""
	}
	codes = make([]string, 0, len(parsed.Error.Errors)+len(parsed.Error.Details))
	for _, e := range parsed.Error.Errors {
		if e.Reason != "" {
			codes = append(codes, e.Reason)
		}
	}
	for _, d := range parsed.Error.Details {
		if d.Reason != "" {
			codes = append(codes, d.Reason)
		}
	}
	return codes, parsed.Error.Status
}

// Reason extracts Google's machine reason code from an error body. It is
// exported because the Google connector that predates this package still runs
// its own transport (Gmail's, which needs a 96 MiB read cap for RAW messages)
// and must name a failure the SAME way — otherwise one disabled-API 403 stays
// diagnosable on calendar and undiagnosable on mail.
//
// It returns "" when the body names no reason or isn't Google's envelope at
// all (an HTML error page from a proxy, say) — an absent reason must never look
// like a present one, so a decode failure yields "" rather than a guess.
func Reason(body []byte) string {
	codes, status := reasonCodes(body)
	// Take the first code that survives validation, not merely the first one
	// present: a value MachineReason refuses must not shadow a usable one that
	// follows it, or the body ends up LESS diagnosable than it really was.
	for _, raw := range codes {
		if r := connector.MachineReason(raw); r != "" {
			return r
		}
	}
	return connector.MachineReason(status)
}

// The rate/quota codes the Gmail and Calendar APIs return: the classic family
// (rateLimitExceeded, userRateLimitExceeded, dailyLimitExceeded) shares the
// "LimitExceeded" suffix, so matching the suffix covers a new per-product RATE
// limit without an edit here; quotaExceeded and the newer ErrorInfo enums have
// their own spellings. The suffix is not a blanket rule for every Google API —
// the Drive family spells permanent capacity errors the same way
// (teamDriveFileLimitExceeded), and those no retry can clear — so a connector
// added to this package for another API must check its own vocabulary rather
// than assume this set.
const (
	limitExceededSuffix = "LimitExceeded"
	// The generic usageLimits reason — "cannot be completed due to access or
	// rate limitations". It is named explicitly because it misses the suffix
	// above by a single capital letter, and a throttle read as a refusal parks
	// the connection.
	reasonLimitExceeded     = "limitExceeded"
	reasonQuotaExceeded     = "quotaExceeded"
	reasonQuotaExceededEnum = "QUOTA_EXCEEDED"
	reasonRateLimitEnum     = "RATE_LIMIT_EXCEEDED"
	reasonResourceExhausted = "RESOURCE_EXHAUSTED"
)

// isRateLimitReason reports whether one parsed reason code names throttling.
func isRateLimitReason(reason string) bool {
	if strings.HasSuffix(reason, limitExceededSuffix) {
		return true
	}
	switch reason {
	case reasonLimitExceeded, reasonQuotaExceeded, reasonQuotaExceededEnum,
		reasonRateLimitEnum, reasonResourceExhausted:
		return true
	default:
		return false
	}
}

// RateLimitBody reports whether a 403 body names throttling rather than a bad
// credential — retryable weather (honor Retry-After, back off) as against a
// rejected grant, which parks the connection until its human reconnects.
//
// It reads the PARSED reason codes, never the raw bytes: a substring scan over
// the whole body also matches the literal sitting in Google's prose message, and
// reading that as throttling means a revoked credential is retried instead of
// being handed back to its human.
//
// A specific reason code, when the body names one, is the WHOLE verdict. The
// status is consulted only when the body names none: this predicate is asked
// about nothing but 403s, where the status is PERMISSION_DENIED by construction —
// the canonical restatement of the HTTP code the caller already branched on — so
// letting it speak alongside a code would veto every throttling verdict. Alone
// it is the only thing the body says, and being no limit it parks.
//
// Among the codes, one we can read and that is NOT a limit vetoes the verdict: a
// body naming both a refusal and a limit is ambiguous, and a refusal is the more
// specific claim. A code we cannot read is no evidence either way rather than
// counter-evidence — the two errors are not equally cheap. Parking a healthy
// connection stops capture until a human re-runs OAuth and nothing else catches
// it, whereas a grant that really is revoked is refused again at the token
// endpoint on the very next sync. So an unreadable field must not be able to
// turn a rate limit into a reauth prompt.
func RateLimitBody(body []byte) bool {
	codes, status := reasonCodes(body)
	if len(codes) == 0 {
		// Nothing specific was named, so the status is all the body says.
		return isRateLimitReason(connector.MachineReason(status))
	}
	limit := false
	for _, raw := range codes {
		switch code := connector.MachineReason(raw); {
		case code == "":
			continue // unreadable — no evidence either way
		case isRateLimitReason(code):
			limit = true
		default:
			return false // a readable refusal: park rather than retry
		}
	}
	return limit
}

// retryAfter parses the provider's Retry-After (delta-seconds form; Google does
// not send HTTP-dates here). Zero when absent — the registry's own backoff then
// takes over.
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// Descriptor is the shared static metadata for a read-only Google capture
// connector: read scope, auto_execute (read-only) tier, produces activities. name is
// the registry key ("gmail", "gcal"). The two Google connectors are identical
// here; a future one that isn't simply builds its own connector.Descriptor.
func Descriptor(name string) connector.Descriptor {
	return connector.Descriptor{
		Name:     name,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Session opens one sync/health pass: it unseals the AuthState and mints a fresh
// access token from the durable refresh token, returning the connected owner
// (the internal-vs-external anchor) and the short-lived access token. A stored
// bundle we cannot read is a corruption, surfaced as an error rather than
// silently treated as a fresh connection.
func Session(ctx context.Context, oauth OAuth, auth connector.Auth) (owner, accessToken string, err error) {
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		return "", "", fmt.Errorf("googleconn: malformed auth state: %w", err)
	}
	access, err := oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return "", "", err
	}
	return st.Owner, access, nil
}

// OAuth is the OAuth2 handshake surface each Google connector supplies to
// Authenticate — the same three-method shape gmail and gcal implement.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (oauthflow.TokenGrant, error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// AuthState is the persisted credential bundle (the opaque connector.Auth). The
// refresh token is the durable secret; the short-lived access token is re-minted
// from it each Sync and never stored. Owner is the connected account's address —
// the internal-vs-external anchor.
type AuthState struct {
	RefreshToken string `json:"refresh_token"`
	Owner        string `json:"owner_email"`
	// Scopes is this system's INTERNAL permission vocabulary (the connector's
	// declared principal scopes), frozen at grant time.
	Scopes []string `json:"scopes"`
	// Granted is what GOOGLE says it granted, in Google's own vocabulary. A
	// separate field because the two vocabularies mean different things and
	// must never overwrite one another; empty for a bundle sealed before the
	// grant was recorded.
	Granted []string `json:"granted_scopes,omitempty"`
}

// authPayload is the connect request the transport hands to Authenticate: the
// OAuth authorization code and the redirect URI it was issued against.
type authPayload struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// AuthRequestFrom packages an OAuth callback's code into the opaque connector
// AuthRequest the callback handler passes to Authenticate.
func AuthRequestFrom(code, redirectURI string) (connector.AuthRequest, error) {
	payload, err := json.Marshal(authPayload{Code: code, RedirectURI: redirectURI})
	if err != nil {
		return connector.AuthRequest{}, fmt.Errorf("googleconn: encoding auth payload: %w", err)
	}
	return connector.AuthRequest{Payload: payload}, nil
}

// ScopeStrings renders principal scopes as the plain strings the AuthState carries.
func ScopeStrings(scopes []principal.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}

// OwnerResolver turns a fresh access token into the connected account's address
// — Gmail's profile emailAddress, Calendar's primary-calendar id. It is the one
// provider-specific step in the otherwise-shared Authenticate handshake.
type OwnerResolver func(ctx context.Context, accessToken string) (string, error)

// Authenticate runs the shared OAuth code→refresh→access→owner handshake and
// returns the sealed AuthState as the opaque connector.Auth. scopes are the
// connector's declared scopes, frozen into the bundle; resolveOwner is the
// per-connector call that names the account. The access token is discarded —
// only the durable refresh token persists.
func Authenticate(ctx context.Context, oauth OAuth, req connector.AuthRequest, scopes []principal.Scope, resolveOwner OwnerResolver) (connector.Auth, error) {
	var p authPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return nil, fmt.Errorf("googleconn: malformed auth payload: %w", err)
	}
	if p.Code == "" {
		return nil, fmt.Errorf("googleconn: authorization code required: %w", ErrAuthRejected)
	}
	grant, err := oauth.Exchange(ctx, p.Code, p.RedirectURI)
	if err != nil {
		return nil, err
	}
	refresh := grant.RefreshToken
	access, err := oauth.AccessToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	owner, err := resolveOwner(ctx, access)
	if err != nil {
		return nil, err
	}
	// An empty owner would make every counterparty look external (ownerDom
	// ""), so an all-internal meeting could be logged in violation of the
	// zero-rows rule (formulas §20). Refuse the connection rather than seal a
	// credential that cannot classify internal-vs-external.
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("googleconn: provider returned an empty account owner: %w", ErrAuthRejected)
	}
	state := AuthState{RefreshToken: refresh, Owner: owner, Scopes: ScopeStrings(scopes), Granted: grant.Scopes}
	//nolint:gosec // G117: sealing the connector's own refresh token into the opaque Auth bundle IS the intended path — the registry stores it encrypted in the vault, never logged or returned
	auth, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("googleconn: encoding auth state: %w", err)
	}
	return auth, nil
}

// AccountLabel reads the authorizing Google account back out of a sealed
// bundle — the connector.AccountLabeler answer both Google connectors share.
// No vault round-trip and no network: the connect already resolved it. A bundle
// that names no account reports none, which the caller stores as an absent
// label rather than an error.
func AccountLabel(auth connector.Auth) (string, error) {
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		return "", fmt.Errorf("googleconn: malformed auth bundle: %w", err)
	}
	return st.Owner, nil
}

// GrantedScopes reads the provider scopes back out of a sealed bundle — the
// connector.GrantedScoper answer both Google connectors share. A bundle sealed
// before the grant was recorded reports none, which the caller stores as an
// absent claim rather than an empty one.
func GrantedScopes(auth connector.Auth) ([]string, error) {
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		return nil, fmt.Errorf("googleconn: malformed auth bundle: %w", err)
	}
	return st.Granted, nil
}
