// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The OAuth wire vocabulary this authorization server both PARSES off a
// request and ADVERTISES in its metadata. Named once, in one place, because
// the two sides have to agree exactly: a metadata document promising a grant
// type or a parameter the endpoints do not honour strands a client after the
// human has already consented, which is the worst possible moment to fail.

const (
	// oauthParamClientID and oauthParamResource are RFC 6749 / RFC 8707
	// request parameters; each is also the name of the field the audit trail
	// and the DCR response carry them under, so the spelling travels.
	oauthParamClientID = "client_id"
	oauthParamResource = "resource"
	// oauthResponseTypeCode is the ONLY response_type this server serves
	// (authorization code + PKCE); there is no implicit flow to widen to.
	oauthResponseTypeCode = "code"
	// oauthRefreshToken is RFC 6749's refresh-token identifier, which the
	// spec reuses across four positions: the advertised grant type, the
	// grant_type of a renewal, the RFC 7009 token_type_hint, and the token
	// response's own member. One spelling for all four.
	oauthRefreshToken = "refresh_token"
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
