// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Where an authorization may be sent back to. Two rules, and both are the
// difference between a working native client and an open redirect: what a
// client may REGISTER (validRedirectURI) and what a registered URI MATCHES at
// authorize time (redirectURIMatches). Split out of oauth.go so each file
// stays one concept — oauth_redirect_test.go is this file's suite.

import "net/url"

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
