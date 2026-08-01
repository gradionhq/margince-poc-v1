// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The A2 handshake end to end (B-EP06.18, B-EP03.14/.15, ADR-0036):
// discovery → DCR (public clients only) → authorize (PKCE S256
// mandatory) → token (single-use code, verifier check, RFC 8707
// audience) → the minted Bearer IS a passport that works on /v1 and on
// the hosted MCP transport — and dies with revocation. Plus the
// ADR-0036 compact JWS on approve: signed, effect-bound, tamper-fatal.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

type oauthEnv struct {
	*connectorEnv
	clientID string
	verifier string
}

const oauthRedirect = "https://client.example/cb"

// setupOAuth arranges a registered public client on the connector harness.
// The authorization server is part of the connector's gated route group
// (mcp_transport_integration_test.go), so this suite runs with the gate ON:
// an installation that never declared the connector serves no /oauth/ at all,
// which is the property that suite asserts.
func setupOAuth(t *testing.T) *oauthEnv {
	t.Helper()
	return setupOAuthWith(t)
}

// setupOAuthWith is setupOAuth plus whatever else one test needs wired, so
// setupOAuth stays the plain deployment posture this suite mostly asserts
// against — the same split setupConnector/setupConnectorWith makes.
func setupOAuthWith(t *testing.T, extra ...compose.Option) *oauthEnv {
	t.Helper()
	e := setupConnectorWith(t, extra...)

	var registered struct {
		ClientID string `json:"client_id"`
	}
	if status := e.call(t, "POST", "/oauth/register", anyMap{
		"client_name": "night agent", "redirect_uris": []string{oauthRedirect},
	}, nil, &registered); status != http.StatusCreated || registered.ClientID == "" {
		t.Fatalf("DCR → %d %+v", status, registered)
	}
	return &oauthEnv{
		connectorEnv: e, clientID: registered.ClientID,
		verifier: strings.Repeat("night-verifier-", 4),
	} // 60 chars, RFC 7636 range
}

func (o *oauthEnv) challenge() string {
	sum := sha256.Sum256([]byte(o.verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeQuery is the baseline authorize request, overridable per field
// by the caller — the one place the query parameters are assembled, so
// authorize and authorizeRaw can never drift against each other.
func (o *oauthEnv) authorizeQuery(extra url.Values) url.Values {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.clientID},
		"redirect_uri":          {oauthRedirect},
		"scope":                 {"read write"},
		"state":                 {"night-state"},
		"code_challenge":        {o.challenge()},
		"code_challenge_method": {"S256"},
	}
	for k, vs := range extra {
		q[k] = vs
	}
	return q
}

// authorizeRaw issues one GET /oauth/authorize and returns the status and
// body verbatim, for a caller asserting on a refusal — authorize's fatal
// "want 200" check would abort the test before the assertion runs.
func (o *oauthEnv) authorizeRaw(t *testing.T, extra url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, o.ts.URL+"/oauth/authorize?"+o.authorizeQuery(extra).Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	return resp.StatusCode, string(body)
}

// consentFragment reads the authorize redirect's fragment parameters — the
// hand-off the SPA consent screen parses, the consent nonce among them. A
// fragment is never transmitted to a server, which is why the nonce rides
// there: the cookie holding its counterpart is Path=/oauth/authorize, so no
// endpoint the screen can call ever receives it.
func consentFragment(t *testing.T, location string) url.Values {
	t.Helper()
	_, fragment, found := strings.Cut(location, "#/oauth-consent?")
	if !found {
		t.Fatalf("authorize did not redirect to the consent screen: %q", location)
	}
	params, err := url.ParseQuery(fragment)
	if err != nil {
		t.Fatalf("parsing the consent fragment %q: %v", fragment, err)
	}
	return params
}

// armConsent drives the authorize GET and returns the form the consent screen
// must POST back — the request's own parameters plus the nonce the redirect
// armed. A GET mints nothing (it must never: OAuth CSRF), so this is only half
// the flow.
func (o *oauthEnv) armConsent(t *testing.T, extra url.Values) url.Values {
	t.Helper()
	status, location, body, _ := o.authorizeNoFollow(t, extra)
	if status != http.StatusFound {
		t.Fatalf("consent redirect → %d %s", status, body)
	}
	nonce := consentFragment(t, location).Get("consent")
	if nonce == "" {
		t.Fatalf("the consent redirect carries no nonce: %q", location)
	}
	form := url.Values{}
	for k, vs := range o.authorizeQuery(extra) {
		form[k] = vs
	}
	form.Set("consent", nonce)
	return form
}

// postConsent posts one consent decision with redirects DISABLED, so the
// redirect toward the client — or the refusal that replaces it — is the
// assertion target rather than whatever it points at.
func (o *oauthEnv) postConsent(t *testing.T, form url.Values) (status int, location, body string) {
	t.Helper()
	post, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	o.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { o.client.CheckRedirect = nil }()
	resp, err := o.client.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the consent POST's body: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

// requestedScopes is the passport vocabulary a scope parameter names, which is
// what a passport minted to be LENT against that request has to carry. It
// mirrors the server's own parse: offline_access asks for the connection's
// lifetime rather than authority over a record, so it is never a passport
// scope, and a request naming no access scope at all defaults to read exactly
// as identity's parseOAuthScopes does.
func requestedScopes(scope string) []string {
	var scopes []string
	for _, sc := range strings.Fields(scope) {
		if sc != "offline_access" {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		return []string{"read"}
	}
	return scopes
}

// authorize drives the whole consent flow the way a human does: the GET hands
// the browser to the consent screen, the human LENDS one of their own
// passports, and the nonce-bound POST is the consent whose redirect carries the
// code.
//
// The lent passport carries exactly the scopes this request asked for, so the
// intersection the connection receives is the request itself: a caller here is
// asserting about the request it made, with the lend contributing no narrowing
// of its own. A test whose subject IS the narrowing mints a passport that
// differs from the request and lends it through approveWithPassport.
func (o *oauthEnv) authorize(t *testing.T, extra url.Values) string {
	t.Helper()
	form := o.armConsent(t, extra)
	form.Set("passport_id", o.mintPassport(t, "lent to the night agent", requestedScopes(form.Get("scope"))))

	status, location, body := o.postConsent(t, form)
	if status != http.StatusFound {
		t.Fatalf("consent POST → %d %s", status, body)
	}
	granted, err := url.Parse(location)
	if err != nil || granted.Query().Get("code") == "" || granted.Query().Get("state") != "night-state" {
		t.Fatalf("redirect malformed: %q", location)
	}
	return granted.Query().Get("code")
}

// closeBody closes a response body and fails the test on a dirty close —
// a broken close can hide a truncated read.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
}

// exchange drives POST /oauth/token for the authorization-code grant and
// returns status + parsed body.
func (o *oauthEnv) exchange(t *testing.T, form url.Values) (int, map[string]any) {
	t.Helper()
	base := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {o.clientID},
		"redirect_uri":  {oauthRedirect},
		"code_verifier": {o.verifier},
	}
	for k, vs := range form {
		base[k] = vs
	}
	return o.postToken(t, base)
}

// postToken posts one form to the token endpoint — the single spelling of
// that exchange, so the code grant and the refresh grant cannot drift apart
// in how the suite drives them.
func (o *oauthEnv) postToken(t *testing.T, form url.Values) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("token response is not JSON: %v", err)
	}
	return resp.StatusCode, body
}

func TestOAuthHandshakeMintsAWorkingPassport(t *testing.T) {
	o := setupOAuth(t)

	// Discovery names the endpoints and S256.
	var metadata struct {
		TokenEndpoint string   `json:"token_endpoint"`
		Methods       []string `json:"code_challenge_methods_supported"`
	}
	if status := o.call(t, "GET", "/.well-known/oauth-authorization-server", nil, nil, &metadata); status != http.StatusOK {
		t.Fatalf("discovery → %d", status)
	}
	if !strings.HasSuffix(metadata.TokenEndpoint, "/oauth/token") || len(metadata.Methods) != 1 || metadata.Methods[0] != "S256" {
		t.Fatalf("discovery document wrong: %+v", metadata)
	}

	// A wrong verifier fails its code…
	badCode := o.authorize(t, nil)
	if status, body := o.exchange(t, url.Values{"code": {badCode}, "code_verifier": {strings.Repeat("wrong-verifier-", 4)}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier → %d %v", status, body)
	}

	// …and the real exchange works exactly once.
	code := o.authorize(t, nil)
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, body)
	}
	token, _ := body["access_token"].(string)
	if !strings.HasPrefix(token, "mgp_") {
		t.Fatalf("access token is not a passport token: %q", token)
	}
	if status, body := o.exchange(t, url.Values{"code": {code}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("code replay → %d %v, want single-use refusal", status, body)
	}

	// The minted Bearer works on the resource surface.
	bearer := map[string]string{"Authorization": "Bearer " + token}
	if status := o.call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("bearer GET /v1/people → %d", status)
	}
}

// The consent gate IS the account-takeover defense: a GET riding an
// existing session must never mint a code, and the consent POST is
// bound to the nonce the form armed.
func TestOAuthConsentGateBlocksSilentAuthorization(t *testing.T) {
	o := setupOAuth(t)
	q := url.Values{
		"response_type": {"code"}, "client_id": {o.clientID},
		"redirect_uri": {oauthRedirect}, "scope": {"read"},
		"code_challenge": {o.challenge()}, "code_challenge_method": {"S256"},
	}
	// GET answers with the consent screen, never a redirect carrying a code.
	req, _ := http.NewRequest(http.MethodGet, o.ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	o.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := o.client.Do(req)
	o.client.CheckRedirect = nil
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(location, "/#/oauth-consent?") {
		t.Fatalf("GET authorize → %d %q, want the consent screen, never a code", resp.StatusCode, location)
	}
	// The redirect goes to the screen, not toward the client — and there is
	// nothing to carry there yet: a GET mints no code.
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)
	// A consent POST without the armed nonce (the cross-site forgery
	// shape) is refused.
	form := url.Values{}
	for k, vs := range q {
		form[k] = vs
	}
	form.Set("consent", "forged")
	post, _ := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = o.client.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged consent POST → %d, want 403", resp.StatusCode)
	}
	// A browser-stamped cross-site POST is refused outright.
	post2, _ := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	post2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post2.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = o.client.Do(post2)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site consent POST → %d, want 403", resp.StatusCode)
	}
}

func TestOAuthRefusesDowngradesAndPrivilegedClients(t *testing.T) {
	o := setupOAuth(t)

	// No confidential clients, ever.
	var problem map[string]any
	if status := o.call(t, "POST", "/oauth/register", anyMap{
		"client_name": "privileged", "redirect_uris": []string{oauthRedirect},
		"token_endpoint_auth_method": "client_secret_basic",
	}, nil, &problem); status != http.StatusBadRequest {
		t.Fatalf("confidential DCR → %d %v, want refusal", status, problem)
	}

	// The plain method and a missing challenge are refused pre-code.
	for name, extra := range map[string]url.Values{
		"plain method": {"code_challenge_method": {"plain"}},
		"no challenge": {"code_challenge": {""}},
	} {
		q := url.Values{
			"response_type": {"code"}, "client_id": {o.clientID},
			"redirect_uri": {oauthRedirect}, "code_challenge": {o.challenge()},
			"code_challenge_method": {"S256"},
		}
		for k, vs := range extra {
			q[k] = vs
		}
		req, _ := http.NewRequest(http.MethodGet, o.ts.URL+"/oauth/authorize?"+q.Encode(), nil)
		resp, err := o.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		closeBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s → %d, want 400", name, resp.StatusCode)
		}
	}

	// RFC 8707: a code bound to the canonical resource refuses another
	// audience at redemption — authorize only ever accepts the
	// canonical value itself (TestAuthorizeRefusesAForeignResourceBeforeMintingACode
	// covers the foreign-audience refusal at authorize).
	code := o.authorize(t, url.Values{"resource": {o.origin + "/mcp"}})
	if status, body := o.exchange(t, url.Values{"code": {code}, "resource": {"https://other.example"}}); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("audience mismatch → %d %v, want invalid_target", status, body)
	}

	// The canonical check is unconditional: a code minted with NO resource
	// at authorize (the accepted older-client path, stored NULL) must still
	// refuse a foreign resource presented only at redemption — the
	// canonical comparison must not depend on a stored value existing.
	nullResourceCode := o.authorize(t, nil)
	if status, body := o.exchange(t, url.Values{"code": {nullResourceCode}, "resource": {"https://attacker.example/mcp"}}); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("foreign resource against a NULL-bound code → %d %v, want invalid_target", status, body)
	}
}

// TestAuthorizeRefusesAForeignResourceBeforeMintingACode proves the RFC 8707
// audience is validated against the configured canonical resource before any
// code exists — a refused audience must mint nothing.
func TestAuthorizeRefusesAForeignResourceBeforeMintingACode(t *testing.T) {
	o := setupOAuth(t)
	status, body := o.authorizeRaw(t, url.Values{"resource": {"https://attacker.example/mcp"}})
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_target") {
		t.Fatalf("authorize with a foreign resource → %d %s, want 400 invalid_target", status, body)
	}
	var codes int
	if err := o.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM oauth_authorization_code`).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	if codes != 0 {
		t.Fatalf("codes = %d, want 0: a refused audience must mint nothing", codes)
	}
}

// assertOwnerCount asserts the SIZE of a row set on the owner pool, which
// QueryRow alone cannot: it silently takes the first of several rows, so
// "exactly one grant" has to be counted, not scanned.
//
//craft:ignore naked-any pgx query arguments are untyped by the driver's own signature
func assertOwnerCount(t *testing.T, o *oauthEnv, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := o.owner.QueryRow(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatalf("counting rows for %s: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

// sha256Hex is how every bearer credential in this schema is stored — the
// test derives the expected hash rather than trusting the row it reads.
func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
