// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// What is and is not a metadata-document client id. The path requirement is the
// load-bearing half: a bare origin as a client id would make one HOST one
// identity, where the whole mechanism depends on one DOCUMENT being one.
func TestOnlyAnHTTPSURLWithAPathIsAMetadataDocumentClientID(t *testing.T) {
	cases := map[string]bool{
		"https://app.example.com/oauth/client.json":  true,
		"https://app.example.com/c":                  true,
		"https://app.example.com":                    false, // no path: one host, one identity
		"https://app.example.com/":                   false, // a bare root is not a document
		"http://app.example.com/client.json":         false, // the profile pins https
		"https://user:pw@app.example.com/client.jsn": false, // userinfo is a second spelling of one id
		"https://app.example.com/c.json#frag":        false, // so is a fragment
		"s6BhdRkqt3":                                 false, // an opaque DCR id
		"":                                           false,
		"https://":                                   false,
		"::not a url":                                false,
	}
	for raw, want := range cases {
		err := cimdClientID(raw)
		if got := err == nil; got != want {
			t.Errorf("cimdClientID(%q) accepted=%v, want %v", raw, got, want)
		}
		if !want && !errors.Is(err, errNotCIMD) {
			t.Errorf("cimdClientID(%q) → %v, want errNotCIMD so the caller falls back to the table", raw, err)
		}
	}
}

// THE security of this mechanism: the document's own client_id must equal the
// URL it was fetched from, byte for byte. Without it, publishing any document
// anywhere lets a client claim to be a client registered here.
func TestADocumentMustClaimTheExactURLItWasFetchedFrom(t *testing.T) {
	const url = "https://app.example.com/client.json"
	cases := map[string]string{
		"a different client entirely": `{"client_id":"https://evil.example/c.json","client_name":"n","redirect_uris":["https://a/cb"]}`,
		"the same URL with a trailing slash": `{"client_id":"https://app.example.com/client.json/","client_name":"n",` +
			`"redirect_uris":["https://a/cb"]}`,
		"the same URL uppercased": `{"client_id":"https://APP.EXAMPLE.COM/client.json","client_name":"n",` +
			`"redirect_uris":["https://a/cb"]}`,
		"no client_id at all": `{"client_name":"n","redirect_uris":["https://a/cb"]}`,
	}
	for name, body := range cases {
		if _, err := validCIMD([]byte(body), url); err == nil {
			t.Errorf("%s: accepted a document that does not claim the URL it came from", name)
		}
	}

	good := `{"client_id":"` + url + `","client_name":"Example","redirect_uris":["https://a/cb"]}`
	doc, err := validCIMD([]byte(good), url)
	if err != nil {
		t.Fatalf("a valid document was refused: %v", err)
	}
	if doc.ClientName != "Example" || len(doc.RedirectURIs) != 1 {
		t.Errorf("the document read back as %+v", doc)
	}
}

// The three REQUIRED members are required. A document missing one is not a
// client this server can describe to a human or redirect back to.
func TestADocumentMissingARequiredMemberIsRefused(t *testing.T) {
	const url = "https://app.example.com/client.json"
	for name, body := range map[string]string{
		"no client_name":      `{"client_id":"` + url + `","redirect_uris":["https://a/cb"]}`,
		"blank client_name":   `{"client_id":"` + url + `","client_name":"  ","redirect_uris":["https://a/cb"]}`,
		"no redirect_uris":    `{"client_id":"` + url + `","client_name":"n"}`,
		"empty redirect_uris": `{"client_id":"` + url + `","client_name":"n","redirect_uris":[]}`,
		"not JSON at all":     `<html>not a metadata document</html>`,
	} {
		if _, err := validCIMD([]byte(body), url); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A redirect uri is held to the SAME rule a DCR registration is. One rule for
// what a redirect may be, whichever door the client came through — otherwise
// CIMD becomes the way to register the redirect DCR refuses.
func TestARedirectInADocumentIsHeldToTheSameRuleAsARegisteredOne(t *testing.T) {
	const url = "https://app.example.com/client.json"
	for _, redirect := range []string{
		"http://evil.example/cb", // plaintext to a remote host
		"javascript:alert(1)",    // not a redirect at all
		"https://a/cb#frag",      // a fragment the client controls after the fact
	} {
		body := `{"client_id":"` + url + `","client_name":"n","redirect_uris":["` + redirect + `"]}`
		if _, err := validCIMD([]byte(body), url); err == nil {
			t.Errorf("redirect %q was accepted from a metadata document but would be refused at registration", redirect)
		}
	}
}

// The fetch's guards, against a real server. Each case is an attack rather than
// a malformed input: an oversized body spends this server's memory, a redirect
// walks past the address guard on its next hop, and a non-JSON answer is a page
// that was never a metadata document.
//
// The egress guard is SWAPPED OUT for the duration, and that is what makes these
// assertions mean anything. netguard refuses a loopback address in the dialer's
// Control hook, and httptest listens on one — so with the real client every case
// below fails at connect time, none of them reaches the handler, and all four
// pass on a guard they are not about. That is exactly the shape of a test that
// cannot fail. The address guard keeps its own test, immediately below, which is
// the only one here that uses the real client.
func TestTheFetchRefusesWhatIsNotAMetadataDocument(t *testing.T) {
	guarded := cimdClient
	cimdClient = &http.Client{Timeout: cimdFetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	t.Cleanup(func() { cimdClient = guarded })

	oversized := strings.Repeat("x", cimdMaxDocument+1)
	cases := map[string]http.HandlerFunc{
		"a body larger than this server reads": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			//craft:ignore swallowed-errors a test server's write failure is the client hanging up
			_, _ = w.Write([]byte(`{"padding":"` + oversized + `"}`))
		},
		"a redirect to somewhere else": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/client.json", http.StatusFound)
		},
		"an HTML page": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			//craft:ignore swallowed-errors a test server's write failure is the client hanging up
			_, _ = w.Write([]byte("<html></html>"))
		},
		"a 404": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
	}
	for name, handler := range cases {
		srv := httptest.NewServer(handler)
		if _, _, err := fetchCIMD(t.Context(), srv.URL+"/client.json"); err == nil {
			t.Errorf("%s: was accepted as a metadata document", name)
		}
		srv.Close()
	}

	// The control, and it is load-bearing: a VALID document must be accepted
	// through the same client. Without it every refusal above could still be a
	// connect failure wearing a different name, which is the defect this test
	// had before the swap.
	valid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//craft:ignore swallowed-errors a test server's write failure is the client hanging up
		_, _ = w.Write([]byte(`{"client_id":"http://` + r.Host + `/client.json","client_name":"n",` +
			`"redirect_uris":["https://a.example/cb"]}`))
	}))
	defer valid.Close()
	if _, _, err := fetchCIMD(t.Context(), valid.URL+"/client.json"); err != nil {
		t.Fatalf("a valid document was refused through the same client (%v); every refusal above proves nothing", err)
	}
}

// A private or loopback address is refused at CONNECT time, on the resolved IP,
// so a public-looking hostname that resolves inward cannot make this server
// probe its own network. httptest listens on 127.0.0.1, which is exactly the
// address the guard exists to refuse — so a successful fetch here would BE the
// vulnerability.
func TestTheFetchRefusesAnAddressInsideTheDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//craft:ignore swallowed-errors a test server's write failure is the client hanging up
		_, _ = w.Write([]byte(`{"client_id":"x","client_name":"n","redirect_uris":["https://a/cb"]}`))
	}))
	defer srv.Close()

	_, _, err := fetchCIMD(t.Context(), srv.URL+"/client.json")

	if err == nil {
		t.Fatal("a metadata document on a loopback address was fetched; an unauthenticated caller can probe this deployment's network")
	}
}

// How long a client's own headers may keep its document believed. The floor
// stops a client from making this server refetch on every authorize — one
// consent page becoming an outbound request the client sets the rate of — and
// the ceiling stops a rotated redirect list from taking a year to be believed.
func TestTheCacheLifetimeIsTheClientsRequestClampedToWhatIsSafe(t *testing.T) {
	cases := map[string]time.Duration{
		"max-age=3600":         time.Hour,
		"public, max-age=7200": 2 * time.Hour,
		"max-age=1":            cimdMinCache, // below the floor
		"max-age=99999999":     cimdMaxCache, // above the ceiling
		"no-store":             cimdMinCache,
		"no-cache":             cimdMinCache,
		"":                     cimdMinCache, // said nothing: shortest, never longest
		"max-age=not-a-number": cimdMinCache,
		"max-age=-500":         cimdMinCache,
	}
	for header, want := range cases {
		if got := cimdCacheTTL(header); got != want {
			t.Errorf("Cache-Control %q → %s, want %s", header, got, want)
		}
	}
}
