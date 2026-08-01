// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RequestOrigin is what the OAuth discovery documents and the advertised MCP
// resource are built from, so a wrong scheme here is not cosmetic: a client
// dereferences the URL exactly as given. Behind a terminating proxy r.TLS is
// nil, which makes X-Forwarded-Proto the ONLY signal — and each hop appends to
// it, so a chain must be read from its first element.
func TestRequestOriginTrustsTheOutermostForwardedProto(t *testing.T) {
	for _, tc := range []struct {
		name, forwarded, want string
		tls                   bool
	}{
		{name: "single https", forwarded: "https", want: "https://crm.example.com"},
		{name: "single http", forwarded: "http", want: "http://crm.example.com"},
		// The chained-proxy shape: the client-facing hop is https, the internal
		// one is plaintext. Reading the whole value matches no scheme and falls
		// back to r.TLS (nil here), advertising http:// to every client.
		{name: "chain keeps the client-facing hop", forwarded: "https, http", want: "https://crm.example.com"},
		{name: "chain without a space", forwarded: "https,http", want: "https://crm.example.com"},
		{name: "chain of three", forwarded: "https, http, http", want: "https://crm.example.com"},
		// The header is a token, so neither case nor padding may decide it.
		{name: "uppercase", forwarded: "HTTPS", want: "https://crm.example.com"},
		{name: "padded", forwarded: "  https  ", want: "https://crm.example.com"},
		// Absent the header the connection itself decides.
		{name: "absent, direct TLS", forwarded: "", tls: true, want: "https://crm.example.com"},
		{name: "absent, plaintext", forwarded: "", want: "http://crm.example.com"},
		// An unparseable value must not be read as a claim of https.
		{name: "garbage falls back to the connection", forwarded: "not-a-scheme", want: "http://crm.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A plaintext target so r.TLS starts nil — httptest.NewRequest sets it
			// for an https target, which would mask the fallback these cases test.
			r := httptest.NewRequest(http.MethodGet, "http://crm.example.com/mcp", nil)
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if got := RequestOrigin(r); got != tc.want {
				t.Fatalf("RequestOrigin(X-Forwarded-Proto: %q) = %q, want %q", tc.forwarded, got, tc.want)
			}
		})
	}
}
