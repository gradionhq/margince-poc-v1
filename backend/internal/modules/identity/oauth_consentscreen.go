// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The hand-off between /oauth/authorize and the consent SCREEN, which lives in
// the SPA. A DECISION goes to the client's own redirect_uri (oauth_redirect.go):
// the code on approve, access_denied on deny — the client is waiting for it.
// Everything else /oauth/authorize can answer a browser with comes back here,
// because a human left looking at a JSON body on a page with no navigation has
// nowhere to go and nothing to click.

import (
	"net/http"
	"net/url"
	"strings"
)

// ConsentScreenPath is the SPA route the authorization flow hands a browser to.
// Exported because a deployment can only be wrong about it silently: the api
// serves no SPA, so nothing here can verify that the same origin answers this
// path, and the failure lands in the middle of a human's consent. The process
// role names it at boot for exactly that reason (compose/mcpedge.go).
//
// It is relative on purpose. The SPA and this api share an origin — the SPA
// reads its API base from location.origin — so naming a host here would add
// configuration that could only ever disagree with reality.
const ConsentScreenPath = "/#/oauth-consent"

// The screen's own vocabulary for a refusal it has to render. These are the
// SPA's words, not RFC 6749's: no client ever sees them, and the screen turns
// each into one sentence in the human's language, which is why the marker
// travels alone and the server's client-facing description does not follow it.
//
// They split in two, and the split decides whether the nonce travels with them:
// a refusal the human's NEXT action can fix keeps the pending authorization
// alive, and one that nothing can fix ends the flow on the screen.
const (
	consentScreenParamError = "error"
	// consentScreenParamNonce is the screen's half of the double-submit pair.
	// Named here rather than in the RFC vocabulary because no client ever sends
	// or reads it: it exists between this endpoint and the SPA alone.
	consentScreenParamNonce = "consent"
	// consentErrorStale: the nonce cookie is gone, expired, or does not match
	// the submitted value. UNRECOVERABLE — a spent or forged nonce fails the
	// same way however often it is presented — so the screen renders a terminal
	// card asking the human to start again from their client, which is the only
	// place a fresh authorization begins.
	consentErrorStale = "stale_consent"
	// consentErrorUnlendable: the selected passport did not survive the
	// re-resolved selectability check, or no usable passport_id was submitted.
	// RECOVERABLE — the pending authorization is untouched and only the human's
	// CHOICE was wrong, so the nonce comes back with the request and their next
	// selection succeeds inside the window the GET already armed.
	consentErrorUnlendable = "unlendable_passport"
	// consentErrorInvalid: validateAuthorize refused the POST — the request the
	// screen posted back is not one this server will authorize any more.
	// UNRECOVERABLE for the same reason: re-posting it would be refused again.
	consentErrorInvalid = "invalid_request"
)

// authorizeScreenParams is the parameter set the consent screen carries and
// posts back: the authorization request itself, and nothing else. Read off
// whatever transported it — the GET's query, the POST's form — so a refusal can
// echo a request it has not validated without inventing fields for it.
var authorizeScreenParams = []string{
	oauthParamResponseType, oauthParamClientID, oauthParamRedirectURI, oauthParamScope,
	oauthParamCodeChallenge, oauthParamCodeChallengeMethod, oauthParamResource, oauthParamState,
}

// consentScreenParams copies one authorize request onto the fragment the screen
// reads. Only the named parameters travel: an echo of the whole incoming query
// would reflect arbitrary attacker-chosen keys into the screen's own state.
//
// The nonce is not among them because it is not part of the REQUEST: it is the
// credential half of the double-submit pair, added back only where the
// authorization it belongs to is still usable (retryAtConsentScreen).
func consentScreenParams(src url.Values) url.Values {
	params := url.Values{}
	for _, name := range authorizeScreenParams {
		params.Set(name, src.Get(name))
	}
	return params
}

// consentHandoffParams is what the screen receives when the authorize GET has
// done its half: the VALIDATED request, plus the nonce that binds the POST to
// the browser reading this redirect.
//
// The nonce can only travel here. Its cookie counterpart is
// Path=/oauth/authorize, so no endpoint the SPA can call ever receives it — the
// screen takes its half of the double-submit pair from the fragment, and the
// POST must present both.
func consentHandoffParams(req authorizeRequest, nonce string) url.Values {
	return url.Values{
		oauthParamResponseType: {oauthResponseTypeCode}, oauthParamClientID: {req.ClientID},
		oauthParamRedirectURI: {req.RedirectURI}, oauthParamScope: {formScope(req)},
		oauthParamCodeChallenge: {req.CodeChallenge}, oauthParamCodeChallengeMethod: {pkceMethodS256},
		oauthParamResource: {req.Resource}, oauthParamState: {req.State},
		consentScreenParamNonce: {nonce},
	}
}

// formScope is the scope string the consent screen must POST back. It re-adds
// offline_access — never a passport scope, and never presented as one — so the
// POST re-derives the same Offline marker; without it the round trip through the
// screen would silently drop the client's refresh request.
//
// offline_access is not authority over any record but over the connection's
// LIFETIME, which the exchange records as the grant's refresh_allowed. That
// audit row asserts the human approved a self-renewing connection, so the
// screen has to disclose it — apart from the scope list, never as an item in
// it, where a human cannot tell it apart from a permission.
func formScope(req authorizeRequest) string {
	scope := strings.Join(req.Scopes, " ")
	if req.Offline {
		return strings.TrimSpace(scope + " " + scopeOfflineAccess)
	}
	return scope
}

// redirectToConsentScreen sends the browser to the screen with the given
// parameters. The single spelling of that destination, so the path, the status
// and the fragment shape cannot drift between the answers that use it.
//
// The params ride in the FRAGMENT, which browsers never transmit: client_id,
// state and the PKCE challenge therefore stay out of this api's access logs and
// out of every intermediary's.
func redirectToConsentScreen(w http.ResponseWriter, r *http.Request, params url.Values) {
	// Not an open redirect: the target is this origin's own SPA route, spelled
	// as a constant, with the caller's values confined to the fragment query.
	http.Redirect(w, r, ConsentScreenPath+"?"+params.Encode(), http.StatusFound)
}

// refuseToConsentScreen answers a refusal nothing can recover from: the nonce is
// spent, or this server will not authorize the request any more. The screen gets
// the request and the marker, WITHOUT a nonce — there is no submission left that
// could succeed, and a screen that offered one would send the human round the
// same refusal for as long as they kept trying.
//
// A refused consent POST is not a client protocol error to report to the client
// — the human is still mid-flow, and their browser is the only thing reading the
// response. Deny and approve still answer the client itself (redirectToClient):
// those are decisions, and the client is waiting for them.
func refuseToConsentScreen(w http.ResponseWriter, r *http.Request, form url.Values, reason string) {
	params := consentScreenParams(form)
	params.Set(consentScreenParamError, reason)
	redirectToConsentScreen(w, r, params)
}

// retryAtConsentScreen answers the refusal the human's next action fixes. The
// pending authorization is still armed and still valid; only the passport they
// picked was not lendable. So the nonce goes back WITH the request, and the
// cookie holding its counterpart is deliberately left alone (clearConsentCookie
// runs where consent COMMITS, not where a nonce was merely presented) — the
// human chooses again and submits inside the same five minutes.
//
// Handing the nonce back discloses nothing new: it rides in the fragment, which
// no browser ever transmits, and its value is what this very browser just sent.
// The double-submit property is untouched, because the POST still requires the
// cookie and the body to agree — this only declines to destroy a pair that is
// still good.
func retryAtConsentScreen(
	w http.ResponseWriter, r *http.Request, form url.Values, nonce, reason string,
) {
	params := consentScreenParams(form)
	params.Set(consentScreenParamError, reason)
	params.Set(consentScreenParamNonce, nonce)
	redirectToConsentScreen(w, r, params)
}
