// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What a HUMAN sees when the consent flow refuses them. The screen is a native
// form in the SPA, so a JSON refusal replaces it with a body of text on a page
// that has no navigation: the human's flow ends there, with no way back and
// nothing to click. Every refusal a human can cause therefore comes back to the
// screen with a marker naming it — and never with the spent nonce, which would
// fail again identically forever.
//
// The refusals a CLIENT causes are unchanged and belong next door: approve and
// deny still answer the client's own redirect_uri (oauth_lend_integration_test.go),
// and a cross-site POST is still refused outright rather than redirected
// (TestOAuthConsentGateBlocksSilentAuthorization).

import (
	"net/http"
	"strings"
	"testing"
)

// consentScreenRefusal reads the marker off the Location a refused consent POST
// answered with, asserting the whole shape the SPA depends on: the screen's own
// route, the authorization request back with it, and the marker naming why.
//
// The spent nonce must be absent, checked twice — as a parameter and as a
// substring of the whole Location. A re-entry carrying it could only be refused
// again for the same reason, so the screen has to obtain a fresh one from
// /oauth/authorize.
func consentScreenRefusal(t *testing.T, status int, location, spentNonce string) string {
	t.Helper()
	if status != http.StatusFound {
		t.Fatalf("refused consent POST → %d, want 302 back to the consent screen", status)
	}
	params := consentFragment(t, location)
	if got := params.Get("consent"); got != "" {
		t.Fatalf("the refusal hands the screen a nonce %q; re-entry must mint a fresh one", got)
	}
	if spentNonce == "" {
		t.Fatal("the caller passed no armed nonce, so the leak check below would pass vacuously")
	}
	if strings.Contains(location, spentNonce) {
		t.Fatalf("the spent nonce leaked into the refusal %q", location)
	}
	// Without the request the screen has nothing to re-post, and the human is
	// back at a form that cannot be submitted.
	for _, param := range []string{"client_id", "redirect_uri", "scope", "code_challenge", "state"} {
		if params.Get(param) == "" {
			t.Fatalf("the refusal dropped %s, which the screen must re-post: %q", param, location)
		}
	}
	return params.Get("error")
}

// A nonce the browser can no longer prove is what a human who left the screen
// open past the cookie's five minutes produces — the ordinary case, not an
// attack. The screen gets them back, and re-entry arms a fresh nonce.
func TestAStaleConsentNonceComesBackToTheScreen(t *testing.T) {
	o := setupOAuth(t)
	form := o.armConsent(t, nil)
	armed := form.Get("consent")
	form.Set("passport_id", o.mintPassport(t, "lendable", []string{"read", "write"}))
	form.Set("consent", "not-the-nonce-this-browser-was-given")

	status, location, _ := o.postConsent(t, form)

	if got := consentScreenRefusal(t, status, location, armed); got != "stale_consent" {
		t.Fatalf("error = %q, want stale_consent: %q", got, location)
	}
	// The nonce check runs before anything durable can exist, and still does.
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)
	assertOwnerCount(t, o, 0,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_authorization_code'`)
}

// A POST whose authorization request no longer validates: the parameters are
// mutated AFTER the nonce was armed, so the double-submit check passes and
// validateAuthorize is the thing that refuses. The human reads the refusal on
// the screen; the specific OAuth code is a client developer's vocabulary and
// stays on the GET, where a client developer looks.
func TestAConsentPostThatFailsValidationComesBackToTheScreen(t *testing.T) {
	o := setupOAuth(t)
	form := o.armConsent(t, nil)
	armed := form.Get("consent")
	form.Set("passport_id", o.mintPassport(t, "lendable", []string{"read", "write"}))
	// The OAuth 2.1 downgrade validateAuthorize refuses: S256 is mandatory.
	form.Set("code_challenge_method", "plain")

	status, location, _ := o.postConsent(t, form)

	if got := consentScreenRefusal(t, status, location, armed); got != "invalid_request" {
		t.Fatalf("error = %q, want invalid_request: %q", got, location)
	}
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)
}

// A human arriving from `claude mcp add` in a fresh browser has no session, and
// this endpoint serves no HTML to ask for one. The SPA does: AuthGate renders
// login in place at the route it was handed, so the answer is the screen —
// carrying the request and nothing this endpoint has not yet done. After signing
// in the screen re-enters /oauth/authorize, which is where a nonce comes from.
func TestAnUnauthenticatedAuthorizeGetRoutesTheHumanToSignIn(t *testing.T) {
	o := setupOAuth(t)

	req, err := http.NewRequest(http.MethodGet,
		o.ts.URL+"/oauth/authorize?"+o.authorizeQuery(nil).Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous := o.sessionlessClient()
	anonymous.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("unauthenticated authorize GET → %d, want 302 to the consent screen (a JSON 401 is a dead end)", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	params := consentFragment(t, location)
	// No nonce: there is no human yet to bind one to, and the screen must
	// re-enter this endpoint after sign-in to obtain one.
	if got := params.Get("consent"); got != "" {
		t.Fatalf("an unauthenticated GET armed a nonce %q", got)
	}
	if len(resp.Cookies()) != 0 {
		t.Fatalf("an unauthenticated GET set cookies %v, so it armed something before knowing who asked", resp.Cookies())
	}
	// The request survives the detour, or signing in loses the connection the
	// human was trying to make.
	if params.Get("client_id") != o.clientID {
		t.Fatalf("client_id = %q, want %q — the request must survive the trip through login", params.Get("client_id"), o.clientID)
	}
	// The target is the SPA document, which this api does not serve: nothing
	// behind this redirect redirects again on its own, so there is no loop.
	if strings.HasPrefix(location, "/oauth/") {
		t.Fatalf("Location = %q points back at the authorization server", location)
	}

	// Where the walk comes to rest: the screen re-enters with exactly the
	// parameters it was handed, now carrying a session, and THAT arms the nonce.
	// Driven from `params` rather than from a freshly built query, so the
	// re-entry is the one the SPA can actually make.
	status, reentry, body, cookies := o.authorizeNoFollow(t, params)
	if status != http.StatusFound {
		t.Fatalf("re-entry with a session → %d %s, want 302", status, body)
	}
	nonce := consentFragment(t, reentry).Get("consent")
	if nonce == "" {
		t.Fatalf("re-entry armed no nonce, so the POST could never satisfy the double-submit check: %q", reentry)
	}
	if got := cookieValue(t, cookies, consentCookieName); got != nonce {
		t.Fatalf("cookie %s = %q, want the fragment's fresh nonce %q", consentCookieName, got, nonce)
	}
}
