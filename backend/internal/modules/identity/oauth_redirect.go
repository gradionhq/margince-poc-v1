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
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
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

// redirectToClient answers the CLIENT at its own registered redirect_uri. Both
// answers a consent decision can produce come through here — the code on
// approval, RFC 6749 §4.1.2.1's access_denied on refusal — so neither can
// forget to echo state, without which a client cannot correlate the answer with
// the request it made.
//
// The answer is MERGED into whatever query the registered URI already carries
// rather than appended behind a second '?': a registered redirect legitimately
// may have one.
func redirectToClient(w http.ResponseWriter, r *http.Request, req authorizeRequest, answer url.Values) {
	location, err := url.Parse(req.RedirectURI)
	if err != nil {
		// validateAuthorize matched this against a redirect_uri that already
		// parsed at registration, so an unparseable one here is this server
		// contradicting itself rather than anything the caller sent.
		httperr.Write(w, r, fmt.Errorf("oauth: registered redirect_uri does not parse: %w", err))
		return
	}
	params := location.Query()
	for key, list := range answer {
		params[key] = list
	}
	if req.State != "" {
		params.Set("state", req.State)
	}
	location.RawQuery = params.Encode()
	// Not an open redirect: the target was matched EXACTLY against the
	// client's registered redirect_uris in validateAuthorize; an unregistered
	// URI never reaches this line.
	http.Redirect(w, r, location.String(), http.StatusFound) // #nosec G710
}
