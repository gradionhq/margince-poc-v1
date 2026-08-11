// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// serving answers one canned response for every path, and reports what was
// asked for. The URL a fetcher builds is as much of its contract as the body it
// accepts.
func serving(t *testing.T, handler http.HandlerFunc) (*url.URL, *[]string) {
	t.Helper()
	asked := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the test origin: %v", err)
	}
	return base, asked
}

func writeHTML(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing the test body: %v", err)
	}
}

func TestFetchAcceptsAWellFormedDocument(t *testing.T) {
	base, asked := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHTML(t, w, cleanDocument)
	})
	got, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI)
	if err != nil {
		t.Fatalf("fetching a well-formed document: %v", err)
	}
	if got != cleanDocument {
		t.Error("the fetcher answered something other than the served body")
	}
	if want := []string{"/mcp-apps/account-brief.html"}; len(*asked) != 1 || (*asked)[0] != want[0] {
		t.Errorf("the fetcher asked for %v, want %v", *asked, want)
	}
}

func TestFetchRefusesARedirect(t *testing.T) {
	// Go follows up to 10 redirects by default, including cross-origin — which
	// would mean the bytes that become executable UI in a user's MCP host came
	// from an origin other than the configured one, and the closed-URL-set
	// fitness test would not constrain it. A first-party constant path should
	// never redirect.
	elsewhere, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHTML(t, w, cleanDocument)
	})
	base, _ := serving(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.String()+"/mcp-apps/account-brief.html", http.StatusFound)
	})
	body, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI)
	if err == nil {
		t.Fatalf("the fetcher followed a redirect off the configured origin and answered %d bytes", len(body))
	}
}

func TestFetchRefusesAnOversizeBody(t *testing.T) {
	// Read at limit+1 rather than exactly the limit: a body read at exactly the
	// cap comes back TRUNCATED and would then be admitted as a short document,
	// which is a silently different view rather than a refusal.
	base, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The write error is DISCARDED here and only here: a refusing fetcher
		// stops reading and hangs up, so a broken pipe part-way through this
		// oversize body is the assertion succeeding, not a fault to report.
		_, _ = io.WriteString(w, cleanDocument+strings.Repeat("<!-- pad -->", 200_000))
	})
	if _, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI); err == nil {
		t.Fatal("the fetcher accepted a body past its cap")
	}
}

func TestFetchAcceptsABodyExactlyAtTheCap(t *testing.T) {
	// The boundary in the other direction, because an off-by-one here refuses a
	// legitimate document and the view simply never appears.
	base, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHTML(t, w, strings.Repeat("x", maxDocumentBytes))
	})
	got, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI)
	if err != nil {
		t.Fatalf("a document exactly at the cap was refused: %v", err)
	}
	if len(got) != maxDocumentBytes {
		t.Errorf("the fetcher answered %d bytes, want %d", len(got), maxDocumentBytes)
	}
}

func TestFetchRefusesANon200(t *testing.T) {
	// An ingress SPA fallback or a CDN error page is the case: both answer a
	// body, and one of them answers it with 200 elsewhere.
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusNoContent} {
		base, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
		})
		_, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI)
		if err == nil {
			t.Errorf("the fetcher accepted HTTP %d as a view", status)
			continue
		}
		if !strings.Contains(err.Error(), http.StatusText(status)) && !strings.Contains(err.Error(), "status") {
			t.Errorf("the error for HTTP %d does not name the status: %v", status, err)
		}
	}
}

func TestFetchRefusesTheWrongContentType(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/plain", ""} {
		base, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			} else {
				// Explicitly nothing: Go would otherwise sniff one from the body.
				w.Header()["Content-Type"] = nil
			}
			if _, err := io.WriteString(w, cleanDocument); err != nil {
				t.Errorf("writing the test body: %v", err)
			}
		})
		if _, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI); err == nil {
			t.Errorf("the fetcher accepted a document served as %q", contentType)
		}
	}
}

func TestFetchAcceptsTheAppProfileAndAParameterisedHTML(t *testing.T) {
	// The media type is PARSED, not compared: a server is entitled to add a
	// charset, and refusing on the parameter would refuse every correct answer
	// nginx gives.
	for _, contentType := range []string{"text/html", "text/html; charset=utf-8", "TEXT/HTML;Charset=UTF-8"} {
		base, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			if _, err := io.WriteString(w, cleanDocument); err != nil {
				t.Errorf("writing the test body: %v", err)
			}
		})
		if _, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI); err != nil {
			t.Errorf("the fetcher refused a document served as %q: %v", contentType, err)
		}
	}
}

func TestFetchRefusesAPlainHTTPOriginThatIsNotLocal(t *testing.T) {
	// An operator running api and web on one private network is a legitimate
	// deployment; the public internet over cleartext is not. The bytes become
	// executable UI in someone's host, so the transport has to be the thing
	// that makes substitution hard.
	base, err := url.Parse("http://views.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if _, err := NewFetcher(base).Fetch(t.Context(), AccountBriefURI); err == nil {
		t.Fatal("the fetcher accepted a public cleartext origin")
	}
}

// TestEveryFetchableURLIsTheConfiguredOriginPlusAConstantPath derives the whole
// reachable set from the catalog and asserts each member is the configured
// origin plus a constant path.
//
// It is what makes the not-netguard argument hold. netguard exists to bound
// TENANT-supplied hosts; here the origin is operator configuration and the path
// a compile-time constant, which is why RefusePrivate would break a legitimate
// deployment rather than protect one. That argument survives only while no
// request input can reach the URL — so a future edit that interpolates something
// a caller chose fails the build rather than review.
func TestEveryFetchableURLIsTheConfiguredOriginPlusAConstantPath(t *testing.T) {
	base, err := url.Parse("https://views.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	fetcher := NewFetcher(base)
	seen := map[string]bool{}
	for _, v := range catalog {
		asked, err := fetcher.documentURL(v.uri)
		if err != nil {
			t.Fatalf("the catalog entry %s has no reachable URL: %v", v.uri, err)
		}
		if got := asked.Scheme + "://" + asked.Host; got != "https://views.example.com" {
			t.Errorf("the view %s is fetched from %s, which is not the configured origin", v.uri, got)
		}
		if asked.RawQuery != "" || asked.Fragment != "" {
			t.Errorf("the view %s is fetched with %q — a constant path carries neither", v.uri, asked.String())
		}
		if seen[asked.String()] {
			t.Errorf("two catalog entries fetch %s; one of them serves the other's document", asked.String())
		}
		seen[asked.String()] = true
	}
	if len(seen) != len(catalog) {
		t.Fatalf("the catalog has %d views but %d reachable URLs", len(catalog), len(seen))
	}
}

func TestFetchRefusesAURIOutsideTheCatalog(t *testing.T) {
	// The closed set, enforced rather than documented: Fetch takes a catalog URI
	// and nothing else, so there is no spelling of a caller-chosen path.
	base, asked := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHTML(t, w, cleanDocument)
	})
	if _, err := NewFetcher(base).Fetch(t.Context(), "ui://margince/../../etc/passwd"); err == nil {
		t.Fatal("the fetcher accepted a URI the catalog does not publish")
	}
	if len(*asked) != 0 {
		t.Errorf("the fetcher made %d request(s) for a URI it should have refused outright", len(*asked))
	}
}

func TestAFetcherWithNoOriginRefusesRatherThanAskingARelativeURL(t *testing.T) {
	// The connector-disabled shape. Nothing should construct a fetcher then, but
	// a nil origin that quietly produced a relative request would retry forever
	// against nothing.
	if _, err := NewFetcher(nil).Fetch(t.Context(), AccountBriefURI); err == nil {
		t.Fatal("a fetcher with no configured origin answered a document")
	}
}
