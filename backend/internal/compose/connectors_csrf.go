// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The account-linking-CSRF nonce of the connector consent flow: a random value
// written to both a SameSite=Lax cookie and the signed state, which the
// callback requires to match. It is what proves the browser completing a
// consent is the browser that started it — the signed state alone only proves
// who started it.

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// oauthCSRFCookie is the base name of the per-flow nonce cookie (SameSite=Lax
// so it rides the top-level redirect back from the provider) that must match
// the nonce in the signed state.
const oauthCSRFCookie = "oauth_csrf"

// stateVersionNamespacedCSRF marks a state minted once the nonce cookie became
// per-provider. A state carrying no version was minted by a build whose cookie
// used the shared name, and its callback has to read THAT name or a consent the
// human already completed at the provider dies on our side. Every state expires
// within connectStateTTL, so once that has elapsed after the deploy no
// version-less state can verify: this constant, legacyCSRFCookieName, and the
// branch selecting it are then dead and go.
const stateVersionNamespacedCSRF = 1

// csrfCookieName names the CSRF nonce cookie for one provider's flow. One
// nonce per provider, because a browser can hold two consent flows open at
// once — a mailbox and a calendar, a Google account and a Microsoft one — and
// a shared name means the second consent silently invalidates the first.
func csrfCookieName(provider string) string {
	return oauthCSRFCookie + "_" + provider
}

// legacyCSRFCookieName is the name a flow started before the namespacing set:
// gmail and graph shared the bare cookie, gcal already carried its suffix.
func legacyCSRFCookieName(provider string) string {
	if provider == providerGmail || provider == providerGraph {
		return oauthCSRFCookie
	}
	return csrfCookieName(provider)
}

// consumeCSRFNonce checks the flow's nonce cookie against the nonce bound into
// the signed state, and clears the cookie once it has matched — the nonce is
// one-shot, and a stale one left behind poisons the next flow. The state's own
// version names which cookie its initiator actually set. False means the
// browser completing this consent is not the browser that started it.
func consumeCSRFNonce(w http.ResponseWriter, r *http.Request, provider string, st connectState) bool {
	name := csrfCookieName(provider)
	if st.Version < stateVersionNamespacedCSRF {
		name = legacyCSRFCookieName(provider)
	}
	csrf, err := r.Cookie(name)
	if err != nil || st.Nonce == "" || subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(st.Nonce)) != 1 {
		slog.WarnContext(r.Context(), "connector callback: CSRF nonce missing/mismatched", "err", err, "provider", provider)
		return false
	}
	// Cleared with the same attributes it was set with, so the delete is honored.
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: "/v1/connectors", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	return true
}
