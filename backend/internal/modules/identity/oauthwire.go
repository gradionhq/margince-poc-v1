// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The OAuth wire vocabulary this authorization server PARSES off a request,
// ADVERTISES in its metadata, and ANSWERS with. Named once, in one place,
// because the sides have to agree exactly: a metadata document promising a
// grant type or a parameter the endpoints do not honour strands a client after
// the human has already consented, which is the worst possible moment to fail.

const (
	// The RFC 6749 / RFC 7636 / RFC 8707 request parameters: the names this
	// server READS off a query or form, and writes back into the redirect that
	// carries an authorization request onward to the consent screen. Every one of
	// them is read in one place and written in another, which is exactly why they
	// are named — a parameter parsed under one spelling and echoed under another
	// strands the round trip in the middle.
	//
	// oauthParamScope doubles as the token response's own `scope` member (RFC 6749
	// §5.1) and oauthParamState as the response parameter a client correlates by
	// (§4.1.2): the spec uses one word for the request and the answer, and so does
	// this server. The audit trail is where that stops — it names client_id,
	// resource and scopes under its own keys (auditField*, passport.go), because
	// an audit payload is not this wire and neither vocabulary may rename the
	// other by accident.
	oauthParamClientID            = "client_id"
	oauthParamResource            = "resource"
	oauthParamScope               = "scope"
	oauthParamState               = "state"
	oauthParamRedirectURI         = "redirect_uri"
	oauthParamResponseType        = "response_type"
	oauthParamCodeChallenge       = "code_challenge"
	oauthParamCodeChallengeMethod = "code_challenge_method"
	// pkceMethodS256 is the ONLY code_challenge_method this server accepts, and
	// the one it advertises. A VALUE, not a parameter name: OAuth 2.1 removed
	// `plain`, so the check that refuses the downgrade and the discovery document
	// that promises the algorithm have to name the same one thing.
	pkceMethodS256 = "S256"
	// oauthResponseTypeCode is the ONLY response_type this server serves
	// (authorization code + PKCE); there is no implicit flow to widen to.
	oauthResponseTypeCode = "code"
	// oauthRefreshToken is RFC 6749's refresh-token identifier, which the
	// spec reuses across four positions: the advertised grant type, the
	// grant_type of a renewal, the RFC 7009 token_type_hint, and the token
	// response's own member. One spelling for all four.
	oauthRefreshToken = "refresh_token"
	// oauthParamError is RFC 6749's error member, which this server writes in
	// two disjoint positions: the §5.2 JSON error body an endpoint answers, and
	// the §4.1.2.1 error redirect a refused authorization sends to the client.
	// One spelling for both, so a refusal cannot be named one thing on one path
	// and something else on the other — and so responseParams (oauth_redirect.go)
	// clears the same member an answer would set.
	oauthParamError = "error"
)

// oauthScopesSupported is the passport vocabulary the metadata advertises,
// least authority first, plus the session-lifetime marker. It is DERIVED from
// passportScopeVocabulary (passport.go) — the same list admission tests
// against — rather than spelled again here, so a scope added to the closed
// vocabulary cannot be left out of discovery. A scope a client cannot see is a
// scope it will never ask for.
//
// offline_access is appended last and is not a member of that vocabulary: it
// asks for the connection's lifetime, not authority over any record, and its
// durable home is oauth_grant.refresh_allowed.
var oauthScopesSupported = func() []string {
	advertised := make([]string, 0, len(passportScopeVocabulary)+1)
	for _, scope := range passportScopeVocabulary {
		advertised = append(advertised, string(scope))
	}
	return append(advertised, scopeOfflineAccess)
}()
