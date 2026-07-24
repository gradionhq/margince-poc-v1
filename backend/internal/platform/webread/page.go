// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// Page is one fetched page with the crawler-facing extras Fetch discards: the
// nav links the raw HTML carried and the raw byte count (for the crawl's total
// byte budget — the stripped text under-counts what was transferred).
type Page struct {
	URL   string
	Text  string
	Links []string
	Bytes int
}

// FetchPage retrieves one page for the crawler: stripped text plus the <a href>
// targets it carried. It requests HTML (never markdown) so link harvesting works
// — the single-page Fetch may return verbatim markdown when a server offers it,
// but the crawler's stripped text is unchanged. The harvest runs on the RAW HTML
// before StripTags (stripping destroys hrefs). Links come back absolute (resolved
// against the page URL), http(s)-only, fragment-free, and deduplicated in
// document order.
func (f *Fetcher) FetchPage(ctx context.Context, rawURL string) (Page, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return Page{}, fmt.Errorf("webread: %q is not a fetchable URL", rawURL)
	}
	body, _, err := f.fetchDoc(ctx, rawURL, "") // no Accept header — the crawler always reads HTML for the link harvest
	if err != nil {
		return Page{}, err
	}
	return Page{
		URL:   rawURL,
		Text:  StripTags(body),
		Links: extractLinks(body, parsed),
		Bytes: len(body),
	}, nil
}

// extractLinks harvests <a href> targets from raw HTML. The tokenizer treats
// <script>/<style> contents as raw text, so an href spelled inside a script
// string is never harvested — only real anchor elements count.
func extractLinks(rawHTML string, base *url.URL) []string {
	tokenizer := html.NewTokenizer(strings.NewReader(rawHTML))
	seen := map[string]bool{}
	var links []string
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			// io.EOF or a malformed tail: either way the parseable prefix has
			// been harvested, which is all a best-effort discovery aid owes.
			return links
		}
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := tokenizer.TagName()
		if string(name) != "a" || !hasAttr {
			continue
		}
		for {
			key, value, more := tokenizer.TagAttr()
			if string(key) == "href" {
				if link, ok := resolveLink(base, string(value)); ok && !seen[link] {
					seen[link] = true
					links = append(links, link)
				}
			}
			if !more {
				break
			}
		}
	}
}

// resolveLink turns one href into an absolute, fragment-free http(s) URL, or
// reports it unusable (mailto:, javascript:, malformed, hostless).
func resolveLink(base *url.URL, href string) (string, bool) {
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false
	}
	abs := base.ResolveReference(ref)
	if (abs.Scheme != "http" && abs.Scheme != "https") || abs.Host == "" {
		return "", false
	}
	abs.Fragment = ""
	return abs.String(), true
}

// FetchSitemap retrieves <origin>/sitemap.xml (robots-checked like any path)
// and returns its <loc> entries. Both shapes parse: a urlset yields page URLs;
// a sitemapindex yields the CHILD SITEMAP URLs as-is — deliberately not
// recursed, the crawl's discovery budget does not chase nested indexes, and
// the caller is expected to ignore entries that are sitemaps rather than
// pages. A missing sitemap (4xx) is an empty list with no error: most sites
// have none, absence is normal.
func (f *Fetcher) FetchSitemap(ctx context.Context, origin string) ([]string, error) {
	sitemapURL := strings.TrimSuffix(origin, "/") + "/sitemap.xml"
	parsed, err := url.Parse(sitemapURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("webread: %q is not a fetchable origin", origin)
	}
	allowed, err := f.pathAllowed(ctx, parsed)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: %s", ErrRobotsDisallowed, parsed.Path)
	}

	body, status, _, err := f.getRaw(ctx, sitemapURL, "")
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		return parseSitemapLocs(body)
	case status >= 400 && status < 500:
		return nil, nil // no sitemap declared — absence is normal
	default:
		return nil, fmt.Errorf("webread: sitemap.xml answered %d", status)
	}
}

// parseSitemapLocs collects every <loc>'s text. Walking the token stream
// instead of unmarshalling a struct lets one pass read both the urlset and
// sitemapindex shapes — the element carrying a <loc> differs, the <loc> does
// not.
func parseSitemapLocs(body string) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(body))
	var locs []string
	inLoc := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return locs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("webread: sitemap.xml is not XML: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			inLoc = element.Name.Local == "loc"
		case xml.EndElement:
			inLoc = false
		case xml.CharData:
			if inLoc {
				if loc := strings.TrimSpace(string(element)); loc != "" {
					locs = append(locs, loc)
				}
			}
		}
	}
}

// SameRegistrableDomain reports whether two URLs' hostnames share an eTLD+1
// (publicsuffix), the "same site" test the crawler's off-domain gate uses:
// blog.acme.de and www.acme.de are both acme.de; acme.de and acme.com are
// not, and neither are two customers of the same co.uk-style suffix. Any
// parse failure answers false — an unparseable URL is never "same site".
func SameRegistrableDomain(a, b string) bool {
	domainA, okA := registrableDomain(a)
	domainB, okB := registrableDomain(b)
	return okA && okB && domainA == domainB
}

func registrableDomain(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(parsed.Hostname()))
	if err != nil {
		return "", false
	}
	return domain, true
}
