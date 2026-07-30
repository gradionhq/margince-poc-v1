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
// nav links the raw HTML carried, the visual-identity assets its <head>
// declared, and the raw byte count (for the crawl's total byte budget — the
// stripped text under-counts what was transferred).
type Page struct {
	URL   string
	Text  string
	Links []string
	Bytes int
	// OGImage is the og:image the page declared (absolute), or "" when it
	// declared none.
	OGImage string
	// Icons are the icons the page's <link rel> declared, in document order.
	Icons []IconRef
}

// Icon rel kinds a page can declare. Callers rank by kind rather than by
// re-parsing rel strings.
const (
	// RelIcon is the standard favicon link (rel="icon" or "shortcut icon").
	RelIcon = "icon"
	// RelAppleTouchIcon is the homescreen icon: usually the largest square
	// raster mark a site publishes.
	RelAppleTouchIcon = "apple-touch-icon"
)

// IconRef is one icon a page's <head> declared: the absolute URL, the rel
// kind that declared it, and the sizes attribute lowercased ("180x180",
// "any", or "" when the page did not say).
type IconRef struct {
	URL   string
	Rel   string
	Sizes string
}

// FetchPage retrieves one page for the crawler: stripped text plus the <a href>
// targets it carried. It requests HTML (never markdown) so link harvesting works
// — the single-page Fetch may return verbatim markdown when a server offers it,
// but the crawler's stripped text is unchanged. The harvest runs on the RAW HTML
// before StripTags (stripping destroys hrefs). Links come back absolute (resolved
// against the page URL), http(s)-only, fragment-free, and deduplicated in
// document order.
func (f *Fetcher) FetchPage(ctx context.Context, rawURL string) (Page, error) {
	if parsed, err := url.Parse(rawURL); err != nil || parsed.Host == "" {
		return Page{}, fmt.Errorf("webread: %q is not a fetchable URL", rawURL)
	}
	// HTML only — the crawler needs HTML for the <a href> link harvest.
	// `base` is where the body CAME from, not where it was asked for: a bare
	// domain redirecting to its www host is the ordinary case, and resolving
	// the page's own relative references against the pre-redirect origin would
	// point every one of them at a host that never served this page.
	body, _, base, err := f.fetchDoc(ctx, rawURL, acceptHTML)
	if err != nil {
		return Page{}, err
	}
	ogImage, icons := extractHeadAssets(body, base)
	return Page{
		URL:     rawURL,
		Text:    StripTags(body),
		Links:   extractLinks(body, base),
		Bytes:   len(body),
		OGImage: ogImage,
		Icons:   icons,
	}, nil
}

// extractHeadAssets harvests the visual identity a page declares: its
// og:image and every <link rel> naming an icon. Both live in attributes
// StripTags destroys, so — like extractLinks — the harvest runs on the raw
// HTML. URLs come back absolute; a candidate whose href does not resolve to
// an http(s) URL is dropped rather than guessed at.
func extractHeadAssets(rawHTML string, base *url.URL) (ogImage string, icons []IconRef) {
	tokenizer := html.NewTokenizer(strings.NewReader(rawHTML))
	seen := map[string]bool{}
	for {
		if tokenizer.Next() == html.ErrorToken {
			// io.EOF or a malformed tail: the parseable prefix has been
			// harvested, which is all a best-effort discovery aid owes.
			return ogImage, icons
		}
		name, hasAttr := tokenizer.TagName()
		if !hasAttr {
			continue
		}
		switch string(name) {
		case "meta":
			if found, ok := ogImageFrom(tokenizer, base); ok && ogImage == "" {
				// First declaration wins: a page repeating og:image offers no
				// basis to rank the repeats, and the first is conventionally
				// the canonical one.
				ogImage = found
			}
		case "link":
			if icon, ok := iconFrom(tokenizer, base); ok && !seen[icon.URL] {
				seen[icon.URL] = true
				icons = append(icons, icon)
			}
		}
	}
}

// tagAttrs collects the current tag's attributes, keys lowercased. The
// tokenizer yields them one at a time and only for the tag it is positioned
// on, so callers that need two attributes together (rel AND href) read the
// whole set first.
func tagAttrs(tokenizer *html.Tokenizer) map[string]string {
	attrs := map[string]string{}
	for {
		key, value, more := tokenizer.TagAttr()
		attrs[strings.ToLower(string(key))] = string(value)
		if !more {
			return attrs
		}
	}
}

// ogImageFrom reads one <meta> as an og:image declaration. Open Graph names
// the field in `property`; some CMSs emit it in `name`, so both are accepted.
func ogImageFrom(tokenizer *html.Tokenizer, base *url.URL) (string, bool) {
	attrs := tagAttrs(tokenizer)
	if attrs["property"] != "og:image" && attrs["name"] != "og:image" {
		return "", false
	}
	return resolveAsset(base, attrs["content"])
}

// iconFrom reads one <link> as an icon declaration.
func iconFrom(tokenizer *html.Tokenizer, base *url.URL) (IconRef, bool) {
	attrs := tagAttrs(tokenizer)
	rel := iconRel(attrs["rel"])
	if rel == "" {
		return IconRef{}, false
	}
	href, ok := resolveAsset(base, attrs["href"])
	if !ok {
		return IconRef{}, false
	}
	return IconRef{URL: href, Rel: rel, Sizes: strings.ToLower(strings.TrimSpace(attrs["sizes"]))}, true
}

// iconRel reduces a rel attribute to the icon kind it declares, or "" for a
// link that declares none. rel is a space-separated token list, so
// "shortcut icon" is an icon. rel="mask-icon" is deliberately not a kind: it
// is a monochrome stencil for Safari's pinned tabs, never the company's mark.
func iconRel(rel string) string {
	for _, token := range strings.Fields(strings.ToLower(rel)) {
		switch token {
		case RelAppleTouchIcon, "apple-touch-icon-precomposed":
			return RelAppleTouchIcon
		case RelIcon:
			return RelIcon
		}
	}
	return ""
}

// resolveAsset resolves a declared asset reference. Unlike a nav link, an
// empty reference is not the page itself — a <link href=""> declares no asset,
// so it is dropped instead of resolving to the page's own URL.
func resolveAsset(base *url.URL, ref string) (string, bool) {
	if strings.TrimSpace(ref) == "" {
		return "", false
	}
	return resolveLink(base, ref)
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
