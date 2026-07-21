// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The crawl's URL judgment, split from the walk itself (sitecrawl.go):
// candidate priority (which discovered URL deserves the next budget
// slot), URL normalization and identity, and the path-keyword page-kind
// classifier the priorities and extraction routing both key off.

import (
	"net/url"
	"strconv"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// Priority bands: probes always lead (their order encodes the one-page-
// per-kind preference), then discovered URLs by their classified kind's
// fact density, boilerplate archives last — deprioritized, never
// excluded, because a small site may publish nothing else.
const (
	priProbe       = 100
	priBoilerplate = 1
	priOther       = 10
)

var kindPriority = map[crmcontracts.SiteReadPageKind]int{
	crmcontracts.SiteReadPageKindImpressum: 70,
	crmcontracts.SiteReadPageKindAbout:     60,
	crmcontracts.SiteReadPageKindTeam:      55,
	crmcontracts.SiteReadPageKindContact:   50,
	crmcontracts.SiteReadPageKindServices:  45,
	crmcontracts.SiteReadPageKindProducts:  40,
}

// depthDemotion ranks an index above its own leaves. /solutions enumerates
// what a company sells — the taxonomy a CRM needs; /solutions/security/
// pen-testing details ONE of those and states its methods and deliverables
// as bullets. Read the leaf first and the budget buys sub-bullets of one
// offering while the list of offerings is never seen at all, which reads
// back as "this company sells affinity mapping" instead of "UX Research".
// A leaf is never excluded — it is simply behind every index above it.
const depthDemotion = 8

// pathDepth counts a URL's path segments on its locale-canonical form, so
// /en/solutions is depth 1 like /solutions and a translation is never
// demoted for carrying a language prefix.
func pathDepth(rawURL string) int {
	parsed, err := url.Parse(localeCanonical(rawURL))
	if err != nil {
		return 1
	}
	depth := 0
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment != "" {
			depth++
		}
	}
	return depth
}

func candidatePriority(cand crawlCandidate) int {
	if cand.probe {
		return priProbe
	}
	if boilerplatePath(cand.url) {
		return priBoilerplate
	}
	pri, ok := kindPriority[classifyKind(cand.url)]
	if !ok {
		pri = priOther
	}
	if demoted := pri - (pathDepth(cand.url)-1)*depthDemotion; demoted > priBoilerplate {
		return demoted
	}
	// A deep page still outranks a blog archive: boilerplate is the only
	// band below everything, and depth alone never demotes into it.
	return priBoilerplate + 1
}

// localePrefixes are the path-leading language tags multilingual sites
// mount translations under. Deliberately an allowlist of common tags,
// not a generic two-letter pattern: a generic match would eat real
// pages like /go or /ai. Ambiguity remains possible (/it can be a
// language or an IT-services page) — the dedupe below only fires when
// the SAME path without the prefix was already read, which keeps the
// false-positive to sites that pair such a page with an identical
// unprefixed one.
var localePrefixes = map[string]bool{
	"en": true, "de": true, "fr": true, "es": true, "it": true, "pt": true,
	"nl": true, "pl": true, "cs": true, "sv": true, "da": true, "no": true,
	"fi": true, "ru": true, "uk": true, "tr": true, "ar": true, "he": true,
	"ja": true, "ko": true, "th": true, "vi": true, "id": true, "ms": true,
	"zh": true, "hi": true, "el": true, "ro": true, "hu": true, "bg": true,
	"en-us": true, "en-gb": true, "de-de": true, "de-at": true, "de-ch": true,
	"zh-cn": true, "zh-tw": true, "pt-br": true, "es-mx": true, "fr-ca": true,
}

// localeCanonical reduces a URL to its language-independent identity:
// the host plus the path with one leading locale segment stripped (and
// the query kept — it may address a distinct document). A URL with no
// locale prefix is its own canonical form.
func localeCanonical(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(segments) > 0 && localePrefixes[strings.ToLower(segments[0])] {
		parsed.Path = "/" + strings.Join(segments[1:], "/")
	}
	parsed.Fragment = ""
	return parsed.String()
}

// boilerplatePath spots archive-shaped URLs (blogs, news, tag/category
// listings, paginated indexes, dated posts) whose pages rarely state
// company facts.
func boilerplatePath(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	for _, marker := range []string{"/blog", "/news", "/tag/", "/category/", "/page/", "/archive"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	// A bare year segment (/2024/…) is the date-archive shape.
	for _, segment := range strings.Split(path, "/") {
		if len(segment) == 4 && (strings.HasPrefix(segment, "19") || strings.HasPrefix(segment, "20")) {
			if _, err := strconv.Atoi(segment); err == nil {
				return true
			}
		}
	}
	return false
}

// normalizeCandidate reduces a discovered URL to its fetchable identity:
// absolute http(s), fragment dropped (a fragment names a position, not a
// different document), tracking parameters stripped (utm_* and click
// ids address analytics, not documents — left in, every campaign
// variant of one page would burn its own budget slot).
func normalizeCandidate(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) {
		return "", false
	}
	parsed.Fragment = ""
	if parsed.RawQuery != "" {
		query := parsed.Query()
		stripped := false
		for key := range query {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "msclkid" {
				query.Del(key)
				stripped = true
			}
		}
		if stripped {
			parsed.RawQuery = query.Encode()
		}
	}
	return parsed.String(), true
}

// classifyKind names what a discovered page probably is, from its path alone.
// Keyword order mirrors the probe list; the first family that matches wins.
func classifyKind(rawURL string) crmcontracts.SiteReadPageKind {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return crmcontracts.SiteReadPageKindOther
	}
	path := strings.ToLower(parsed.Path)
	switch {
	case containsAny(path, "impressum", "imprint", "legal"):
		return crmcontracts.SiteReadPageKindImpressum
	case containsAny(path, "about", "ueber"):
		return crmcontracts.SiteReadPageKindAbout
	case strings.Contains(path, "team"):
		return crmcontracts.SiteReadPageKindTeam
	case containsAny(path, "kontakt", "contact"):
		return crmcontracts.SiteReadPageKindContact
	case containsAny(path, "service", "leistung", "solution", "loesung", "lösung"):
		return crmcontracts.SiteReadPageKindServices
	case containsAny(path, "produkt", "product"):
		return crmcontracts.SiteReadPageKindProducts
	default:
		return crmcontracts.SiteReadPageKindOther
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
