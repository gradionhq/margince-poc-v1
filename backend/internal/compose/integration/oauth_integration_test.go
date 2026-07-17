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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

	"github.com/gradionhq/margince/backend/internal/compose"
)

type oauthEnv struct {
	*env
	clientID string
	verifier string
}

const oauthRedirect = "https://client.example/cb"

func setupOAuth(t *testing.T) *oauthEnv {
	t.Helper()
	e := setup(t)
	e.slug = "oauth-e2e"
	bootstrapWorkspaceSession(t, e, "OAuth E2E", "granter@fable.test")

	var registered struct {
		ClientID string `json:"client_id"`
	}
	if status := e.call(t, "POST", "/oauth/register", anyMap{
		"client_name": "night agent", "redirect_uris": []string{oauthRedirect},
	}, nil, &registered); status != http.StatusCreated || registered.ClientID == "" {
		t.Fatalf("DCR → %d %+v", status, registered)
	}
	return &oauthEnv{env: e, clientID: registered.ClientID,
		verifier: strings.Repeat("night-verifier-", 4)} // 60 chars, RFC 7636 range
}

func (o *oauthEnv) challenge() string {
	sum := sha256.Sum256([]byte(o.verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize drives the consent flow: GET renders the approval form (a
// GET must never mint a code — OAuth CSRF), the nonce-bound POST is
// the consent, and the redirect carries the code.
func (o *oauthEnv) authorize(t *testing.T, extra url.Values) string {
	t.Helper()
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
	req, err := http.NewRequest(http.MethodGet, o.ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("consent form → %d %s", resp.StatusCode, body)
	}
	nonce := regexp.MustCompile(`name="consent" value="([^"]+)"`).FindSubmatch(body)
	if nonce == nil {
		t.Fatalf("consent form carries no nonce: %s", body)
	}

	form := url.Values{}
	for k, vs := range q {
		form[k] = vs
	}
	form.Set("consent", string(nonce[1]))
	post, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	o.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { o.client.CheckRedirect = nil }()
	resp, err = o.client.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("consent POST → %d %s", resp.StatusCode, body)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || location.Query().Get("code") == "" || location.Query().Get("state") != "night-state" {
		t.Fatalf("redirect malformed: %q", resp.Header.Get("Location"))
	}
	return location.Query().Get("code")
}

// closeBody closes a response body and fails the test on a dirty close —
// a broken close can hide a truncated read.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
}

// exchange drives POST /oauth/token and returns status + parsed body.
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
	req, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/token", strings.NewReader(base.Encode()))
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
	// GET answers the form, never a redirect carrying a code.
	req, _ := http.NewRequest(http.MethodGet, o.ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
		t.Fatalf("GET authorize → %d %q, want the consent form, never a code", resp.StatusCode, resp.Header.Get("Location"))
	}
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

	// RFC 8707: a code bound to one resource refuses another audience.
	code := o.authorize(t, url.Values{"resource": {"https://mcp.margince.example"}})
	if status, body := o.exchange(t, url.Values{"code": {code}, "resource": {"https://other.example"}}); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Fatalf("audience mismatch → %d %v, want invalid_target", status, body)
	}
}

func TestApprovalTokenIsASignedEffectBoundJWS(t *testing.T) {
	o := setupOAuth(t)

	code := o.authorize(t, nil)
	_, body := o.exchange(t, url.Values{"code": {code}})
	agentBearer := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}

	var person struct {
		ID string `json:"id"`
	}
	if status := o.call(t, "POST", "/v1/people", anyMap{"full_name": "JWS Target"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if status := o.call(t, "DELETE", "/v1/people/"+person.ID, nil, agentBearer, &problem); status != http.StatusForbidden {
		t.Fatalf("agent archive → %d, want staged 403", status)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	var approved struct {
		ApprovalToken *string `json:"approval_token"`
	}
	if status := o.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, &approved); status != http.StatusOK {
		t.Fatalf("approve → %d", status)
	}
	if approved.ApprovalToken == nil || strings.Count(*approved.ApprovalToken, ".") != 2 {
		t.Fatalf("approve response lacks a compact JWS: %+v", approved.ApprovalToken)
	}

	pool, err := database.NewPool(context.Background(), envDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var wsRaw string
	if err := o.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, o.slug).Scan(&wsRaw); err != nil {
		t.Fatal(err)
	}
	wsID, err := ids.Parse(wsRaw)
	if err != nil {
		t.Fatal(err)
	}
	wsCtx := principal.WithWorkspaceID(context.Background(), wsID)

	svc := approvals.NewService(pool)
	claims, err := svc.VerifyApprovalToken(wsCtx, *approved.ApprovalToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.ApprovalID.String() != approvalID || claims.Kind != "archive_record" ||
		claims.TargetID == nil || claims.DiffHash == "" || claims.PassportID == nil {
		t.Fatalf("claims not effect-bound: %+v", claims)
	}

	// One flipped payload byte is fatal.
	parts := strings.Split(*approved.ApprovalToken, ".")
	tampered := parts[0] + "." + flipLastChar(parts[1]) + "." + parts[2]
	if _, err := svc.VerifyApprovalToken(wsCtx, tampered); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("tampered token → %v, want ErrApprovalTokenInvalid", err)
	}
}

func TestHostedMCPTransportSharesTheGovernedSurface(t *testing.T) {
	o := setupOAuth(t)
	code := o.authorize(t, nil)
	_, body := o.exchange(t, url.Values{"code": {code}})
	token := body["access_token"].(string)

	pool, err := database.NewPool(context.Background(), envDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	authSvc := identity.NewService(pool)
	registry := compose.NewRegistry(pool)
	authenticate := func(r *http.Request) (context.Context, error) {
		wsID, err := authSvc.InstallationWorkspace(r.Context())
		if err != nil {
			return nil, err
		}
		ctx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		agent, err := authSvc.AuthenticateAgent(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}
	hosted := httptest.NewServer(agents.NewHTTPHandler(registry, authenticate, "margince-crm", "test"))
	t.Cleanup(hosted.Close)

	rpc := func(bearer, payload string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, hosted.URL, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer closeBody(t, resp)
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	status, out := rpc(token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != http.StatusOK || !strings.Contains(out, `"search_records"`) {
		t.Fatalf("hosted tools/list → %d %s", status, out)
	}
	status, out = rpc(token, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_record","arguments":{"record_type":"person","fields":{"full_name":"Hosted Agent Person"}}}}`))
	if status != http.StatusOK || !strings.Contains(out, "Hosted Agent Person") {
		t.Fatalf("hosted tools/call → %d %s", status, out)
	}

	// Revocation binds between two calls: kill the passport via the
	// session surface, the next hosted call answers 401 + RFC 9728.
	var passportID string
	if err := o.owner.QueryRow(context.Background(),
		`SELECT id FROM passport ORDER BY created_at DESC LIMIT 1`).Scan(&passportID); err != nil {
		t.Fatal(err)
	}
	if status := o.call(t, "DELETE", "/v1/passports/"+passportID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke → %d", status)
	}
	req, _ := http.NewRequest(http.MethodPost, hosted.URL, strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "oauth-protected-resource") {
		t.Fatalf("revoked bearer → %d %q, want 401 + RFC 9728 pointer", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func envDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set")
	}
	return dsn
}

func flipLastChar(s string) string {
	last := s[len(s)-1]
	replacement := byte('A')
	if last == 'A' {
		replacement = 'B'
	}
	return s[:len(s)-1] + string(replacement)
}
