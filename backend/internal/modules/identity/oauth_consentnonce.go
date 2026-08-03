// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The consent nonce's whole life: armed by the authorize GET, proven by the
// consent POST, retired when a decision commits. It lives in one file because
// the three moments only make sense together — the cookie's flags must not
// drift between arming and clearing, and WHEN it is cleared is the difference
// between a refusal the human can act on and a dead end.

import (
	"crypto/subtle"
	"net/http"
)

// consentCookie carries the double-submit nonce that binds the consent POST to
// the browser that saw the consent screen. SameSite=Strict means a cross-site
// attacker can neither read nor ride it, and Path=/oauth/authorize keeps it off
// every route the SPA can call — the screen holds only the fragment's half.
const consentCookie = "crm_oauth_consent"

// consentCookieTTL is the whole window a pending authorization gets: generous
// for a human reading a consent screen, short enough that an abandoned one
// expires on its own. It is the authorization's lifetime, not one POST's — the
// human may need a second attempt (a passport revoked in another tab is refused
// and asked again), so only a committed decision ends it early.
const consentCookieTTL = 300

// armConsentCookie stores the server's half of the pair for one authorization.
func armConsentCookie(w http.ResponseWriter, nonce string) {
	setConsentCookie(w, nonce, consentCookieTTL)
}

// clearConsentCookie retires the armed nonce. Called where consent COMMITS —
// the approve that minted a code, the deny that answered the client — and
// nowhere else: those are the two points after which no further POST for this
// authorization can be legitimate. Clearing it merely because a nonce was
// PRESENTED would destroy the pair a recoverable refusal has to hand back,
// leaving the human at a screen whose only action can never succeed.
func clearConsentCookie(w http.ResponseWriter) {
	setConsentCookie(w, "", -1)
}

// setConsentCookie is the ONE spelling of the cookie's attributes, so arming and
// clearing cannot disagree about the path or the flags — a clear written against
// a different Path leaves the original cookie in place, and the browser then
// keeps proving a nonce the server believes it retired.
func setConsentCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: consentCookie, Value: value, Path: authorizePath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// consentNonceNowProven reports whether this POST presents BOTH halves of the
// pair, comparing them in constant time. The cookie is HttpOnly and scoped to
// this endpoint, so a page on another origin can neither read it nor learn the
// fragment's copy: only the browser that was handed the consent screen can
// satisfy both at once.
//
// A missing or empty cookie is a failure, never a pass — an expired nonce and a
// never-armed one are the same fact, and treating "nothing to compare" as
// agreement would make the whole check optional.
func consentNonceNowProven(r *http.Request) bool {
	cookie, err := r.Cookie(consentCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	submitted := r.PostForm.Get(consentScreenParamNonce)
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(submitted)) == 1
}
