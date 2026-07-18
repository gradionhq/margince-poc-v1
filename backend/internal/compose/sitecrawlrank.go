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

func candidatePriority(cand crawlCandidate) int {
	if cand.probe {
		return priProbe
	}
	if boilerplatePath(cand.url) {
		return priBoilerplate
	}
	if pri, ok := kindPriority[classifyKind(cand.url)]; ok {
		return pri
	}
	return priOther
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
	case containsAny(path, "service", "leistung"):
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
