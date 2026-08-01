// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package freemail answers one question: is this mail domain a consumer
// mailbox provider rather than a company's own domain? A yes means no
// organization may be derived from it — alice@gmail.com is a person, "Gmail" is
// not her employer.
//
// Two modules need the same answer from opposite ends of the capture path:
// capture's tier ladder gates ingestion, and people's counterparty ensure is the
// chokepoint every creation route enters. Neither may import the other, and a
// second spelling of the list would be a second answer.
//
// It sits in platform rather than shared for two reasons that agree: it owns no
// domain — mail addressing is plumbing, not CRM — and the public-suffix list it
// needs is a third-party dependency, which the Tier-0 purity rule forbids.
package freemail

import (
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Registrable normalizes a mail domain to its registrable eTLD+1 via the
// public-suffix list: "eu.docusign.net" → "docusign.net", "news.acme.co.uk" →
// "acme.co.uk". Unicode labels fold to punycode, which is what a mail header
// actually carries. Returns "" for a blank domain, and the input unchanged when
// no registrable label can be derived (a bare hostname, an intranet label) — an
// honest passthrough, never a guess.
func Registrable(domain string) string {
	domain = normalize(domain)
	if domain == "" {
		return ""
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		// A bare public suffix ("co.uk") or a label with no known suffix. Neither
		// can be narrowed further, so the caller gets what it gave.
		return domain
	}
	return base
}

// Hostname returns the registrable form of a domain only when the input is a
// syntactically valid hostname, reporting false otherwise.
//
// Mail domains arrive from a From: header, which is forgeable and which
// net/mail parses far more loosely than DNS allows: `jane@%` parses, and `%` is
// a legal RFC 5322 atext character. A domain that reaches SQL, a crawl seed, or
// an organization_domain row without passing here is a string an outsider
// chose — and one of those uses put it in a LIKE pattern, where `%` matched
// every address in the workspace.
//
// This is the gate; callers that only need normalization keep using Registrable.
func Hostname(domain string) (string, bool) {
	base := Registrable(domain)
	if base == "" || len(base) > maxHostnameLen || !strings.Contains(base, ".") {
		return "", false
	}
	for _, label := range strings.Split(base, ".") {
		if !validHostnameLabel(label) {
			return "", false
		}
	}
	return base, true
}

// maxHostnameLen is the DNS limit on a presentation-format name.
const maxHostnameLen = 253

// validHostnameLabel accepts one DNS label: letters, digits and inner hyphens,
// 1..63 characters. Registrable has already lowercased and punycoded, so this
// judges ASCII only and a Unicode domain is judged by its xn-- form.
func validHostnameLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// normalize lowercases, trims, drops the root dot, and folds Unicode labels to
// punycode. The IDNA conversion is the lookup profile deliberately: a domain
// that fails it is returned as-is rather than dropped, because this is also the
// entry point for the LIST's own entries, and silently discarding one would
// shrink the gate without saying so.
func normalize(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(domain), ".")))
	if domain == "" {
		return ""
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return domain
	}
	return ascii
}

// Matcher decides consumer-provider membership against the pinned baseline (the
// vendored dataset plus this repo's own pins) with a deployment overlay: extra
// adds domains the baseline misses, never carves out a false positive. never
// wins over everything, including the baseline — it is the escape hatch for the
// one B2B host a shipped list gets wrong, and an operator locked out by the
// baseline has no other way back in.
type Matcher struct {
	extra map[string]struct{}
	never map[string]struct{}
}

// New builds a matcher over the baseline with the overlay applied. Both slices
// may be nil; blank entries are ignored so a half-filled config list cannot
// widen the gate to every domain.
func New(extra, never []string) *Matcher {
	return &Matcher{extra: normalizedSet(extra), never: normalizedSet(never)}
}

// IsConsumer reports whether mail from this domain comes from a consumer
// mailbox provider, so no organization may be derived from it.
//
// The domain is matched at every level from itself down to its registrable
// eTLD+1, so "mail.gmx.net" hits the listed "gmx.net" without the list
// enumerating subdomains, while a list entry that IS a subdomain under a
// multi-label public suffix ("biz.ezweb.ne.jp") still matches exactly. The walk
// stops at the registrable domain: matching a bare public suffix would make
// every ".de" address consumer mail.
func (m *Matcher) IsConsumer(domain string) bool {
	domain = normalize(domain)
	if domain == "" {
		return false
	}
	base, pinned := Registrable(domain), baseline()
	for candidate := domain; ; {
		if _, carved := m.never[candidate]; carved {
			return false
		}
		if _, hit := pinned[candidate]; hit {
			return true
		}
		if _, hit := m.extra[candidate]; hit {
			return true
		}
		if candidate == base {
			return false
		}
		_, rest, found := strings.Cut(candidate, ".")
		if !found || rest == "" {
			return false
		}
		candidate = rest
	}
}

// normalizedSet folds a configured list into a lookup set, dropping blanks.
func normalizedSet(domains []string) map[string]struct{} {
	set := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		if d = normalize(d); d != "" {
			set[d] = struct{}{}
		}
	}
	return set
}
