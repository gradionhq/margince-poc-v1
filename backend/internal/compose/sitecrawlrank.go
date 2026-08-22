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
	companyWord    = "company"
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

// legalIdentityPath recognizes pages that can plausibly state the
// operator's registered identity. A broad "contains legal" rule turns a
// product page such as /teams/legal into an imprint and lets whole policy
// libraries consume the crawl and profile budgets. Keep this deliberately
// path-shaped: named imprint/publisher pages, a shallow /legal landing, and
// the two observed shallow site conventions that mount it one level down.
func legalIdentityPath(rawURL string) bool {
	parsed, err := url.Parse(localeCanonical(rawURL))
	if err != nil {
		return false
	}
	segments := pathSegments(parsed.Path)
	if len(segments) == 0 {
		return false
	}
	last := segments[len(segments)-1]
	if containsAny(last, "impressum", "imprint") || legalNoticeSegment(last) {
		return true
	}
	// "publisher" is an imprint only at the TOP of a site, because it is also
	// an ordinary industry a company sells to. arvato.com publishes
	// /industries/publisher in four languages, and reading those as legal
	// pages cost the profile lane its whole budget: six of its 38 pages were
	// counted as legal, which both starves the commercial evidence the
	// profile is built from and votes in the multi-entity census that decides
	// whether the legal fields are withheld at all.
	if last == "publisher" {
		return len(segments) == 1
	}
	if last != "legal" {
		return false
	}
	return len(segments) == 1 || (len(segments) == 2 && (segments[0] == "c" || segments[0] == companyWord))
}

// legalNoticeSegments are the mandatory-legal-notice page names this
// classifier recognises beyond the German and English forms above.
//
// Every one of them names the SAME legally required page an Impressum is —
// France's mentions légales, Spain's aviso legal, Italy's note legali — so
// treating them as legal authority widens the reader's reach without
// loosening what counts as legal evidence.
//
// Deliberately NOT here: contact, about, company. Those are the pages the
// non-German half of the dataset actually publishes, and a registered
// address taken off a marketing page is exactly the guess the legal gate
// exists to refuse. Widening the gate to reach them would trade the
// no-guess property for coverage.
var legalNoticeSegments = map[string]bool{
	"legal-notice":            true,
	"legal-notices":           true,
	"legalnotice":             true,
	"mentions-legales":        true,
	"mentions-légales":        true,
	"aviso-legal":             true,
	"note-legali":             true,
	"nota-legal":              true,
	"colofon":                 true,
	"juridische-kennisgeving": true,
}

func legalNoticeSegment(last string) bool {
	return legalNoticeSegments[last]
}

func pathSegments(path string) []string {
	var out []string
	for _, segment := range strings.Split(strings.ToLower(path), "/") {
		if segment != "" {
			out = append(out, segment)
		}
	}
	return out
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

// The words a path uses to name its about, team and contact pages.
//
// German and English only, until this: a Vietnamese site mounts the same page
// at /gioi-thieu and a Korean one at /회사소개, and neither matched, so both were
// classified `other` and the reader never looked for staff on them. It is not
// a small effect on a dataset with an Asian half — vinatechgroup.vn names its
// chairman on /gioi-thieu, and the only page that DID classify was the English
// /en/about-us, which names nobody. The company then read as publishing no
// staff at all.
//
// EXACT-matched, not substring-matched, and that difference is the whole
// design. The German and English words above stay on containsAny because years
// of paths have shown what they do. The added ones cannot: a substring test on
// them is demonstrably wrong, and so is a prefix test.
//
//	/gioi-thieu-san-pham-moi  "introducing our new PRODUCT" -> about
//	/acme-introduces-widget   a press release               -> about
//	/nhan-su/tuyen-dung       a JOBS page                   -> team
//	/임직원복지                staff BENEFITS                 -> team
//	/신년인사말                a new-year greeting            -> about
//	/alienhero                a product name                -> contact
//
// Every one of those was produced by earlier versions of this list. A false
// `about` is worse than a miss: the profile lane spends a limited page budget
// on what this function names, so a wrong classification starves the
// commercial evidence rather than merely failing to help.
//
// A prefix does not save it. Vietnamese says what it is introducing right
// after the verb, so `gioi-thieu-cong-ty` ("introducing the company") and
// `gioi-thieu-san-pham` ("introducing the product") share every leading
// character that matters. Separating them needs a Vietnamese noun list, which
// is a bigger claim than this classifier should make on a path alone.
//
// So the list holds the forms the corpus actually publishes, matched whole. It
// reaches every real page in the dataset — a Vietnamese site names the page
// plainly or appends its own company name — and admits nothing else.
//
// Deliberately NOT here at all: `company`, `info`, `introduce`, `nhan-su`,
// `인사말`. The first two are ordinary section words. `introduce` is an English
// verb before it is a page name. `nhan-su` ("personnel") and `인사말`
// ("greeting") name a subject rather than a staff directory. That is the
// argument legalNoticeSegments already makes for keeping contact and about out
// of the LEGAL gate, one level down.
var (
	// vi: gioi thieu = "introduction", ve chung toi = "about us".
	// ko: 회사소개 = "company introduction".
	aboutSegments = map[string]bool{
		"gioi-thieu": true, "gioithieu": true,
		"gioi-thieu-cong-ty": true, "ve-chung-toi": true,
		"회사소개": true,
	}
	// vi: doi ngu = "the team". ko: 조직도 = "org chart", 임직원 = "executives
	// and staff" — whole only, so 임직원복지 (staff benefits) is not a directory.
	teamSegments = map[string]bool{
		"doi-ngu": true, "doingu": true,
		"조직도": true, "임직원": true,
	}
	// vi: lien he = "contact". ko: 연락처 = "contact details", 오시는길 = "how to
	// find us", which is where a Korean site prints its address.
	contactSegments = map[string]bool{
		"lien-he": true, "lienhe": true,
		"연락처": true, "오시는길": true,
	}
)

// namedSegment matches a path segment against one of the whole-word sets,
// tolerating the file extension a static site appends: the corpus publishes
// both `lien-he` and `lien-he.html`, and both name the same page.
//
// It also accepts `<word>-<company name>`, which is the one qualifier a real
// about page carries — `gioi-thieu-han-my-viet`, `gioi-thieu-ve-tth-automation`
// — recognised by matching the site's own host label rather than by guessing
// at Vietnamese grammar.
func namedSegment(segment, host string, words map[string]bool) bool {
	segment = strings.TrimSuffix(strings.TrimSuffix(segment, ".html"), ".htm")
	if words[segment] {
		return true
	}
	label := hostLabel(host)
	if label == "" {
		return false
	}
	// Compared with the hyphens removed from BOTH sides, because a path spells
	// a company name in words where the domain runs them together:
	// hanmyviet.vn publishes /gioi-thieu-han-my-viet.
	flat := strings.ReplaceAll(segment, "-", "")
	label = strings.ReplaceAll(label, "-", "")
	for word := range words {
		word = strings.ReplaceAll(word, "-", "")
		if flat == word+label || flat == word+"ve"+label {
			return true
		}
	}
	return false
}

// hostLabel is the site's own name as a path would spell it: the registrable
// label with its dots turned into hyphens, so han-my-viet.vn yields
// "han-my-viet" and tth-automation.com yields "tth-automation".
func hostLabel(host string) string {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	host = strings.TrimPrefix(host, "www.")
	label, _, _ := strings.Cut(host, ".")
	return label
}

// classifyKind names what a discovered page probably is, from its path alone.
// Keyword order mirrors the probe list; the first family that matches wins.
func classifyKind(rawURL string) crmcontracts.SiteReadPageKind {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return crmcontracts.SiteReadPageKindOther
	}
	segments := pathSegments(parsed.Path)
	if len(segments) > 0 && localePrefixes[segments[0]] {
		segments = segments[1:]
	}
	first := ""
	if len(segments) > 0 {
		first = segments[0]
	}
	shallow := len(segments) <= 2
	switch {
	case legalIdentityPath(rawURL):
		return crmcontracts.SiteReadPageKindImpressum
	case shallow && (containsAny(first, "about", "ueber") ||
		namedSegment(first, parsed.Host, aboutSegments)):
		return crmcontracts.SiteReadPageKindAbout
	case shallow && (containsAny(first, "team", "leadership") ||
		namedSegment(first, parsed.Host, teamSegments)):
		return crmcontracts.SiteReadPageKindTeam
	case shallow && (containsAny(first, "kontakt", "contact") ||
		namedSegment(first, parsed.Host, contactSegments)):
		return crmcontracts.SiteReadPageKindContact
	case containsAny(first, "service", "leistung", "solution", "loesung", "lösung"):
		return crmcontracts.SiteReadPageKindServices
	case containsAny(first, "produkt", "product"):
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
