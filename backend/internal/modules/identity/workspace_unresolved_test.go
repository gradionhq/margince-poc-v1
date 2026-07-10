// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The public auth paths stay reachable when the tenant slug resolves to
// nothing (the middleware binds no workspace so bootstrap keeps working).
// Each of them must answer its protocol's client error, never a 500 —
// and the credential surfaces must not disclose whether the workspace
// exists: a login or code exchange against a tenant that isn't there
// reads exactly like one against bad credentials.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func workspacelessHandlers() Handlers {
	// The guards under test fire before any SQL, so a zero Service (nil
	// pool) proves no database round-trip happens on these paths.
	return NewHandlers(&Service{}, nil)
}

func TestLoginAgainstUnresolvedWorkspaceReadsLikeBadCredentials(t *testing.T) {
	h := workspacelessHandlers()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"someone@example.test","password":"whatever-password"}`))
	rec := httptest.NewRecorder()

	h.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("login body is not a problem document: %v", err)
	}
	// The same detail the wrong-password branch writes — the response must
	// not distinguish "workspace absent" from "credentials wrong".
	if problem.Detail != "invalid email or password" {
		t.Errorf("login detail = %q, want the bad-credentials wording", problem.Detail)
	}
}

func TestLogoutAgainstUnresolvedWorkspaceStaysIdempotent(t *testing.T) {
	h := workspacelessHandlers()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "stale-token"}) // #nosec G124 -- request-side cookie: AddCookie sends name=value only; Secure/HttpOnly are response attributes
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout left the session cookie standing; want it cleared")
	}
}

func TestOAuthTokenAgainstUnresolvedWorkspaceReadsLikeSpentCode(t *testing.T) {
	h := workspacelessHandlers()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"some-authorization-code"},
		"code_verifier": {"some-code-verifier"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.oauthToken(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("token status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("token body is not an oauth error document: %v", err)
	}
	// invalid_grant is what a spent or unknown code answers — a code in a
	// workspace that doesn't resolve must be indistinguishable from it.
	if body.Error != "invalid_grant" {
		t.Errorf("token error = %q, want %q", body.Error, "invalid_grant")
	}
}

func TestOAuthRegisterAgainstUnresolvedWorkspaceIsRefused(t *testing.T) {
	h := workspacelessHandlers()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"probe","redirect_uris":["https://client.example/cb"]}`))
	rec := httptest.NewRecorder()

	h.oauthRegister(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("register body is not an oauth error document: %v", err)
	}
	if body.Error != "invalid_request" {
		t.Errorf("register error = %q, want %q", body.Error, "invalid_request")
	}
}
