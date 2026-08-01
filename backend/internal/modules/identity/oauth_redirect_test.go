// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/url"
	"testing"
)

func TestRedirectURIMatchingIsPortAgnosticForLoopbackOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		registered, presented string
		want                  bool
	}{
		// Claude Code declares http://localhost/callback and connects from an
		// ephemeral port; RFC 8252 §7.3 requires the port be ignored.
		"loopback, ephemeral port":     {"http://localhost/callback", "http://localhost:3118/callback", true},
		"loopback, ip literal":         {"http://127.0.0.1/callback", "http://127.0.0.1:51872/callback", true},
		"loopback, registered w/ port": {"http://127.0.0.1:8080/callback", "http://127.0.0.1:9999/callback", true},
		"loopback, different path":     {"http://localhost/callback", "http://localhost:3118/other", false},
		"loopback, different scheme":   {"http://localhost/callback", "https://localhost/callback", false},
		// A public host must still match exactly — port-agnostic matching
		// there would widen the redirect surface.
		"public host, port differs":   {"https://claude.ai/api/mcp/auth_callback", "https://claude.ai:8443/api/mcp/auth_callback", false},
		"public host, exact":          {"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback", true},
		"public host, different host": {"https://claude.ai/cb", "https://evil.example/cb", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := redirectURIMatches(tc.registered, tc.presented); got != tc.want {
				t.Fatalf("redirectURIMatches(%q, %q) = %v, want %v", tc.registered, tc.presented, got, tc.want)
			}
		})
	}
}

// The delivered Location carries OUR answer and only our answer. A client's
// own query survives, but every parameter an authorization response is made of
// comes from this server — RFC 6749 §4.1.2 and §4.1.2.1 are disjoint
// responses, and a client reads whichever it looks for first, so a `code`
// beside an `error`, or a `state` it never sent, makes it act on an answer
// nobody gave.
func TestClientResponseURICarriesOnlyOurAnswer(t *testing.T) {
	approval := url.Values{"code": {"AUTHCODE"}}
	refusal := url.Values{"error": {"access_denied"}}

	for name, tc := range map[string]struct {
		redirectURI string
		state       string
		answer      url.Values
		want        string
	}{
		// The two answers, on the plain redirect every client registers.
		"approval": {
			"https://client.example/cb", "S", approval,
			"https://client.example/cb?code=AUTHCODE&state=S",
		},
		"refusal": {
			"https://client.example/cb", "S", refusal,
			"https://client.example/cb?error=access_denied&state=S",
		},

		// A query of the client's own is the reason this merges rather than
		// appending behind a second '?', and it must come through untouched.
		"the client's own query is preserved": {
			"https://client.example/cb?tenant=acme", "S", approval,
			"https://client.example/cb?code=AUTHCODE&state=S&tenant=acme",
		},

		// A preset response parameter is cleared, whichever it is: for a
		// loopback client the presented URI's query is never validated, so
		// these arrive from whoever composed the authorize request.
		"a preset code cannot survive a refusal": {
			"https://client.example/cb?code=preset", "S", refusal,
			"https://client.example/cb?error=access_denied&state=S",
		},
		"a preset error cannot survive an approval": {
			"https://client.example/cb?error=access_denied", "S", approval,
			"https://client.example/cb?code=AUTHCODE&state=S",
		},
		"a preset error_description and error_uri are cleared too": {
			"https://client.example/cb?error_description=nope&error_uri=https%3A%2F%2Fe.example", "S", approval,
			"https://client.example/cb?code=AUTHCODE&state=S",
		},
		"a preset state never reaches the client": {
			"https://client.example/cb?state=pinned", "S", approval,
			"https://client.example/cb?code=AUTHCODE&state=S",
		},

		// No state sent means no state delivered — not an empty one, and
		// certainly not one the redirect_uri carried.
		"absent state emits no state at all": {
			"https://client.example/cb", "", approval,
			"https://client.example/cb?code=AUTHCODE",
		},
		"absent state does not license the URI's own": {
			"https://client.example/cb?state=pinned", "", approval,
			"https://client.example/cb?code=AUTHCODE",
		},

		// validRedirectURI refuses a fragment at registration; matching is
		// port/scheme/host/path only, so a loopback client can present one
		// anyway. It must not ride into the Location.
		"a smuggled fragment is dropped": {
			"http://localhost:7777/cb#evil", "S", approval,
			"http://localhost:7777/cb?code=AUTHCODE&state=S",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := clientResponseURI(
				authorizeRequest{RedirectURI: tc.redirectURI, State: tc.state}, tc.answer)
			if err != nil {
				t.Fatalf("clientResponseURI(%q): %v", tc.redirectURI, err)
			}
			if got != tc.want {
				t.Fatalf("clientResponseURI(%q, state=%q) = %q, want %q", tc.redirectURI, tc.state, got, tc.want)
			}
		})
	}
}

// A query this server cannot decode is refused rather than delivered: silently
// dropping the pair that failed would hand the client a callback URL it never
// registered. Registration refuses the same shape, so this is the closed door
// behind that one.
func TestClientResponseURIRefusesAQueryItCannotReproduce(t *testing.T) {
	const malformed = "https://client.example/cb?%zz=1&ok=2"

	if validRedirectURI(malformed) {
		t.Fatalf("validRedirectURI(%q) = true, want false: an undecodable query must be refused at registration", malformed)
	}
	got, err := clientResponseURI(
		authorizeRequest{RedirectURI: malformed, State: "S"}, url.Values{"code": {"AUTHCODE"}})
	if err == nil {
		t.Fatalf("clientResponseURI(%q) = %q, want an error rather than a mangled redirect", malformed, got)
	}
}

// A query a client may legitimately register still registers.
func TestValidRedirectURIAdmitsADecodableQuery(t *testing.T) {
	const withQuery = "https://client.example/cb?tenant=acme"
	if !validRedirectURI(withQuery) {
		t.Fatalf("validRedirectURI(%q) = false, want true", withQuery)
	}
}
