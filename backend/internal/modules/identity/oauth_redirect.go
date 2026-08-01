// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Where an authorization may be sent back to, and how. Three rules, and the
// first two are the difference between a working native client and an open
// redirect: what a client may REGISTER (validRedirectURI), what a registered
// URI MATCHES at authorize time (redirectURIMatches), and how a decision is
// actually delivered to the address that survived both (redirectToClient).
// Split out of oauth.go so each file stays one concept —
// oauth_redirect_test.go is this file's suite.

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// validRedirectURI admits https anywhere and plain http only on
// loopback (native-app dev flows).
//
// A query is allowed — a client may legitimately register one — but only one
// this server can reproduce verbatim when it delivers a response there. An
// undecodable query is refused HERE, where a client developer sees it at
// registration, rather than at the redirect that follows a human's consent.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

// redirectURIMatches compares a registered redirect URI with a presented one.
// Non-loopback URIs must match exactly. Loopback URIs match ignoring the PORT
// (RFC 8252 §7.3): a native client binds an ephemeral port per session, so an
// exact comparison refuses every CLI client — Claude Code, Cursor, MCP
// Inspector and mcp-remote all behave this way.
func redirectURIMatches(registered, presented string) bool {
	if registered == presented {
		return true
	}
	reg, err := url.Parse(registered)
	if err != nil {
		return false
	}
	pres, err := url.Parse(presented)
	if err != nil {
		return false
	}
	if !isLoopbackHost(reg.Hostname()) || !isLoopbackHost(pres.Hostname()) {
		return false
	}
	return reg.Scheme == pres.Scheme && reg.Hostname() == pres.Hostname() && reg.Path == pres.Path
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// redirectToClient answers the CLIENT at its own redirect_uri. Both answers a
// consent decision can produce come through here — the code on approval, RFC
// 6749 §4.1.2.1's access_denied on refusal — so neither can forget to echo
// state, without which a client cannot correlate the answer with the request it
// made.
func redirectToClient(w http.ResponseWriter, r *http.Request, req authorizeRequest, answer url.Values) {
	location, err := clientResponseURI(req, answer)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// Not an open redirect: the target was matched against the client's
	// registered redirect_uris in validateAuthorize; an unregistered URI never
	// reaches this line.
	http.Redirect(w, r, location, http.StatusFound) // #nosec G710
}

// responseParams is every parameter an authorization RESPONSE is made of (RFC
// 6749 §4.1.2 and §4.1.2.1). Each is deleted from the redirect_uri's own query
// before the answer is applied, so all of them in the delivered Location come
// from this server and none from the URI the client presented.
//
// Success and error are DISJOINT responses in the RFC, and a client reads
// whichever it looks for first: a `code` sitting beside an `error` — or a
// `state` the client never sent — makes it act on an answer nobody gave. This
// is not merely a registration mistake either. redirectURIMatches compares
// scheme, host and path only, so for a loopback client the presented URI's
// query is never validated and a preset response parameter is reachable by
// whoever composes the authorize request.
var responseParams = []string{"code", oauthParamError, "error_description", "error_uri", "state"}

// clientResponseURI is the absolute Location one authorization response is
// delivered at: the client's redirect_uri carrying our answer, its own query
// otherwise preserved, and nothing of its own that could be mistaken for part
// of the answer.
func clientResponseURI(req authorizeRequest, answer url.Values) (string, error) {
	location, err := url.Parse(req.RedirectURI)
	if err != nil {
		// validateAuthorize matched this against a redirect_uri that already
		// parsed at registration, so an unparseable one here is this server
		// contradicting itself rather than anything the caller sent.
		return "", fmt.Errorf("oauth: redirect_uri does not parse: %w", err)
	}
	// A query we cannot parse is refused rather than delivered: the alternative
	// is silently dropping the pair that failed to decode, which hands the
	// client a callback URL it did not register. validRedirectURI refuses such
	// a registration, so this is the closed door behind that one.
	params, err := url.ParseQuery(location.RawQuery)
	if err != nil {
		return "", fmt.Errorf("oauth: redirect_uri query does not parse: %w", err)
	}
	for _, name := range responseParams {
		params.Del(name)
	}
	for key, list := range answer {
		params[key] = list
	}
	// An absent state means NO state parameter, not an empty one: a client that
	// sent none must not be handed one to compare against.
	if req.State != "" {
		params.Set("state", req.State)
	}
	location.RawQuery = params.Encode()
	// validRedirectURI refuses a fragment at registration; the same rule holds
	// at delivery, so a fragment smuggled onto a presented loopback URI cannot
	// ride into the Location. URL.String() emits a fragment only when Fragment
	// is set, which is why clearing that one field is enough.
	location.Fragment = ""
	return location.String(), nil
}
