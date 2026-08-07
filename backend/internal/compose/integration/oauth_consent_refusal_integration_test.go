// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What a HUMAN sees when the consent flow refuses them. The screen is a native
// form in the SPA, so a JSON refusal replaces it with a body of text on a page
// that has no navigation: the human's flow ends there, with no way back and
// nothing to click. Every refusal a human can cause therefore comes back to the
// screen with a marker naming it.
//
// Whether the nonce comes back with it is the whole distinction between the two
// kinds: a refusal the human's next action can fix keeps the armed pair alive
// (consentScreenRetry), and one that nothing can fix hands back the request
// alone, because a form it could submit would only be refused again
// (consentScreenRefusal).
//
// The refusals a CLIENT causes are unchanged and belong next door: approve and
// deny still answer the client's own redirect_uri (oauth_lend_integration_test.go),
// and a cross-site POST is still refused outright rather than redirected
// (TestOAuthConsentGateBlocksSilentAuthorization).

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// consentScreenHandback reads the fragment a refused consent POST answered with
// and asserts what EVERY refusal owes the screen: the screen's own route, and the
// authorization request back with it — without which the human is at a form with
// nothing to re-post.
func consentScreenHandback(t *testing.T, status int, location string) url.Values {
	t.Helper()
	if status != http.StatusFound {
		t.Fatalf("refused consent POST → %d, want 302 back to the consent screen", status)
	}
	params := consentFragment(t, location)
	for _, param := range []string{"client_id", "redirect_uri", "scope", "code_challenge", "state"} {
		if params.Get(param) == "" {
			t.Fatalf("the refusal dropped %s, which the screen must re-post: %q", param, location)
		}
	}
	return params
}

// consentScreenRefusal reads the marker off a TERMINAL refusal: one the human
// cannot act on, where the screen states that the connection has to be started
// again from the client.
//
// The nonce must be absent, checked twice — as a parameter and as a substring of
// the whole Location. A re-entry carrying it could only be refused again for the
// same reason, so a screen offered it would present an action that never works.
func consentScreenRefusal(t *testing.T, status int, location, spentNonce string) string {
	t.Helper()
	params := consentScreenHandback(t, status, location)
	if got := params.Get("consent"); got != "" {
		t.Fatalf("a terminal refusal hands the screen a nonce %q; nothing it could submit would be accepted", got)
	}
	if spentNonce == "" {
		t.Fatal("the caller passed no armed nonce, so the leak check below would pass vacuously")
	}
	if strings.Contains(location, spentNonce) {
		t.Fatalf("the spent nonce leaked into the refusal %q", location)
	}
	return params.Get("error")
}

// consentScreenRetry reads the marker off a RECOVERABLE refusal, where only the
// human's choice was wrong. The armed nonce has to come back with the request:
// the pending authorization is still valid, and a screen without the nonce could
// only render a selection whose submission the double-submit check must refuse.
func consentScreenRetry(t *testing.T, status int, location, armed string) string {
	t.Helper()
	params := consentScreenHandback(t, status, location)
	if armed == "" {
		t.Fatal("the caller passed no armed nonce, so the check below would pass vacuously")
	}
	if got := params.Get("consent"); got != armed {
		t.Fatalf("a recoverable refusal handed back nonce %q, want the armed %q — without it the screen's only action fails",
			got, armed)
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

// A passport revoked in another tab is the case this hand-back exists for, and
// the only refusal a human can act on: the pending authorization is untouched, so
// the flow must SURVIVE it — the nonce comes back on the fragment, its cookie
// counterpart is left armed, and the second selection is accepted inside the
// window the GET already opened.
//
// The second POST is deliberately assembled from the REFUSAL's own parameters
// rather than from a freshly armed request, because that is the only form the
// screen can build. A refusal that strips the nonce, and a POST that clears the
// cookie merely because a nonce was presented, each leave the human at a selector
// whose only button lands on the stale-consent dead end — and neither is visible
// to a test that stops at "the refusal came back with a marker".
func TestAnUnlendablePassportLeavesTheHumanASecondChoice(t *testing.T) {
	o := setupOAuth(t)
	revoked := o.mintPassport(t, "revoked in another tab", []string{"read"})
	o.revokePassport(t, revoked)
	lendable := o.mintPassport(t, "still lendable", []string{"read"})

	form := o.armConsent(t, url.Values{"scope": {"read"}})
	armed := form.Get("consent")
	form.Set("passport_id", revoked)

	status, location, _ := o.postConsent(t, form)

	if got := consentScreenRetry(t, status, location, armed); got != "unlendable_passport" {
		t.Fatalf("error = %q, want unlendable_passport: %q", got, location)
	}
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)

	// The human picks another passport and submits the form they were handed.
	second := consentScreenHandback(t, status, location)
	second.Del("error")
	second.Set("passport_id", lendable)

	status, location, body := o.postConsent(t, second)

	if status != http.StatusFound || !strings.HasPrefix(location, oauthRedirect) {
		t.Fatalf("the second lend → %d %q %s, want a 302 to the client: the refusal must not have spent the consent",
			status, location, body)
	}
	granted, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing the client redirect %q: %v", location, err)
	}
	code := granted.Query().Get("code")
	if code == "" {
		t.Fatalf("the second lend redirected to %q with no code", location)
	}
	// A real consent, not merely a redirect that looks like one: the code
	// redeems, and the lend recorded is the passport the human ended up choosing
	// — never the one that was refused.
	if tokenStatus, tokenBody := o.exchange(t, url.Values{"code": {code}}); tokenStatus != http.StatusOK {
		t.Fatalf("redeeming the second lend's code → %d %v", tokenStatus, tokenBody)
	}
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM audit_log
		WHERE entity_type = 'oauth_authorization_code' AND after->>'passport_id' = $1`, lendable)
}

// The other end of that cookie's life. A refusal leaves the armed pair alone, so
// something has to retire it — and that something is the DECISION: the approve
// that minted a code and the deny that answered the client each clear the cookie
// on their way out, which is what stops one armed authorization from being
// submitted over and over for the rest of its five minutes.
func TestACommittedConsentCannotBeSubmittedTwice(t *testing.T) {
	o := setupOAuth(t)
	for name, decision := range map[string]struct {
		decide func(*testing.T, *oauthEnv, url.Values)
		answer string
	}{
		"an approve that minted a code": {
			decide: func(t *testing.T, o *oauthEnv, form url.Values) {
				t.Helper()
				form.Set("passport_id", o.mintPassport(t, "lent once", []string{"read"}))
			},
			answer: "code=",
		},
		"a deny that answered the client": {
			decide: func(t *testing.T, _ *oauthEnv, form url.Values) {
				t.Helper()
				form.Set("deny", "1")
			},
			answer: "error=access_denied",
		},
	} {
		t.Run(name, func(t *testing.T) {
			form := o.armConsent(t, url.Values{"scope": {"read"}})
			armed := form.Get("consent")
			decision.decide(t, o, form)

			status, location, body := o.postConsent(t, form)
			if status != http.StatusFound || !strings.Contains(location, decision.answer) {
				t.Fatalf("the decision → %d %q %s, want a 302 carrying %s", status, location, body, decision.answer)
			}

			// The very same form again, nonce and all. The browser no longer holds
			// the cookie half of the pair, so this is refused — and refused
			// TERMINALLY, because there is nothing left to decide.
			status, location, _ = o.postConsent(t, form)
			if got := consentScreenRefusal(t, status, location, armed); got != "stale_consent" {
				t.Fatalf("re-submitting a committed consent → %q, want stale_consent: %q", got, location)
			}
		})
	}
	// One code per approve, however many times the form was submitted: the deny
	// subtest above mints none at all, so this is the approve's single row.
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_authorization_code`)
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
		o.TS.URL+"/oauth/authorize?"+o.authorizeQuery(nil).Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous := o.sessionlessClient()
	anonymous.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer apptest.CloseBody(t, resp)

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
