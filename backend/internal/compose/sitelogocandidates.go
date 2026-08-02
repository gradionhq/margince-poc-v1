// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The logo candidate chain: which assets a resolve will ask a site for, in
// which order, and how the fixed budget is spent between the icons a page
// DECLARED and the two site-level sources every site has anyway. What becomes
// of a fetched candidate — the shape test, the normalization, the store — is
// sitelogo.go; this file never fetches anything.

import (
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// logoCandidates builds the ordered, deduplicated candidate chain from what
// the seed page declared plus the well-known favicon path every site answers
// whether it declares one or not.
//
// A candidate may live on ANOTHER host, and that is a deliberate departure
// from the crawl's off-domain rule (sitecrawlwave.go, which refuses to follow
// page content off the seed's site). It is a departure because a mark
// routinely is CDN-hosted — afs.de serves its logo from CloudFront, stripe.com
// from its asset host — and refusing those would leave exactly the companies
// with the most deliberate branding wearing a monogram.
//
// What makes the departure narrow rather than an open relay: the fetch is a
// GET of BYTES that are only ever decoded as an image, never read as content
// and never followed; the target host's own robots.txt still governs it; the
// SSRF dialer still refuses non-public addresses; and the chain is bounded to
// logoMaxCandidates fetches of maxAssetBytes each, so one read's whole asset
// egress is bounded whatever a page declares. Every candidate tried, off-host
// ones included, is named in the report.
func logoCandidates(seedURL string, declared declaredAssets) (candidates []string, dropped int) {
	seen := make(map[string]bool, len(declared.icons)+2)
	keepNew := func(into []string, urls ...string) []string {
		for _, u := range urls {
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			into = append(into, u)
		}
		return into
	}

	icons := keepNew(nil, iconURLsByRel(declared.icons, webread.RelAppleTouchIcon)...)
	icons = keepNew(icons, iconURLsByRel(declared.icons, webread.RelIcon)...)

	// The two site-level sources: what every site has whether it declared
	// anything or not, and — last — the share image, which on a small site
	// usually IS the mark.
	var candidateFallbacks []string
	if wellKnown, ok := wellKnownFaviconURL(seedURL); ok {
		candidateFallbacks = append(candidateFallbacks, wellKnown)
	}
	if declared.ogImage != "" {
		candidateFallbacks = append(candidateFallbacks, declared.ogImage)
	}

	// The cap bounds one read's asset egress, so it has to bite somewhere —
	// but it must not bite the fallbacks. They are exactly what answers when
	// the declarations are stale, and a page carrying logoMaxCandidates dead
	// touch-icon tags would otherwise spend the whole budget on them and
	// leave the company with no mark at all.
	//
	// The reserve is held first and released after: a fallback that one of the
	// SURVIVING declarations already named needs no slot of its own, and the
	// slot goes back to the next declaration. Deduping against a declaration
	// the cap CUT would be the bug this reserve exists to prevent — the
	// fallback would vanish into a candidate nothing ever fetches.
	reserved := take(icons, logoMaxCandidates-len(candidateFallbacks))
	out := appendMissing(reserved, candidateFallbacks)
	// The reserve that came back, spent on the declarations the first cut left
	// out — never on the ones already in.
	out = appendMissing(out, take(icons[len(reserved):], logoMaxCandidates-len(out)))

	inOut := 0
	for _, icon := range icons {
		if slices.Contains(out, icon) {
			inOut++
		}
	}
	return out, len(icons) - inOut
}

// take copies the first n of urls, or all of them, never fewer than none.
//
// A COPY: a plain urls[:n] shares its backing array, and the first append onto
// it would overwrite urls[n] — the next declaration would silently become
// whatever was appended.
func take(urls []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if n > len(urls) {
		n = len(urls)
	}
	return append([]string(nil), urls[:n]...)
}

// appendMissing adds the urls `into` does not already carry, in order.
func appendMissing(into []string, urls []string) []string {
	out := into
	for _, u := range urls {
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// iconURLsByRel selects one rel's icons, largest declared size first. A page
// that declares several sizes of the same icon is telling us which is the
// detailed one; a page that declares no size at all sorts last, because a
// stated 180x180 is better evidence than a shrug.
func iconURLsByRel(icons []webread.IconRef, rel string) []string {
	matching := make([]webread.IconRef, 0, len(icons))
	for _, icon := range icons {
		if icon.Rel == rel {
			matching = append(matching, icon)
		}
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return declaredIconEdge(matching[i].Sizes) > declaredIconEdge(matching[j].Sizes)
	})
	urls := make([]string, 0, len(matching))
	for _, icon := range matching {
		urls = append(urls, icon.URL)
	}
	return urls
}

// declaredIconEdge reads the largest edge out of a sizes attribute
// ("32x32", "16x16 32x32", "any"), or 0 when it states nothing usable —
// "any" means a scalable source, which says nothing about pixels. Both sides
// of each token count: a rare non-square declaration is ranked by its longer
// edge, which is what "largest" has to mean for the ordering to hold.
func declaredIconEdge(sizes string) int {
	largest := 0
	for _, token := range strings.Fields(sizes) {
		width, height, found := strings.Cut(token, "x")
		if !found {
			continue
		}
		for _, side := range []string{width, height} {
			if edge, err := strconv.Atoi(side); err == nil && edge > largest {
				largest = edge
			}
		}
	}
	return largest
}

// wellKnownFaviconURL is /favicon.ico on the seed's own origin: the icon a
// site serves whether or not it ever declared one, and the last thing worth
// asking for before falling back to the monogram.
func wellKnownFaviconURL(seedURL string) (string, bool) {
	parsed, err := url.Parse(seedURL)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host + "/favicon.ico", true
}
