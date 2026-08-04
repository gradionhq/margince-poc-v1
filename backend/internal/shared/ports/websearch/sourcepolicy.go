// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package websearch

// The allowed-source policy (ADR-0081 §3). Discovery is open; fetching is
// gated.
//
// This is the one place that answers "may we fetch this URL", and it lives in
// the port rather than in a caller so every consumer of the seam inherits the
// same answer. A policy each feature re-implements is a policy that drifts,
// and the first drift is the one that ends up in a deposition.
//
// What it does NOT do is decide whether a result may be CITED. A LinkedIn URL
// is a perfectly good citation and a forbidden fetch — the profile page is
// where the claim lives, and saying so costs nobody anything. Conflating the
// two would throw away the metadata that makes this seam useful.

import (
	"net/url"
	"strings"
)

// deniedHosts are the platforms this product never fetches, whatever a search
// returns.
//
// The reason is contract law rather than data protection, and the distinction
// matters because the two have different remedies: these platforms' terms
// prohibit automated collection, and being public data does not touch that.
// LinkedIn's sanctioned channels stay what ADR-0078 §8 made them — the
// member's own portability export and CSV — and this seam does not add a
// third by the back door.
//
// Subdomains are covered: the check is suffix-based, so `de.linkedin.com`
// is as denied as `www.linkedin.com`.
var deniedHosts = []string{
	"linkedin.com",
	"xing.com",
	"facebook.com",
	"instagram.com",
	"x.com",
	"twitter.com",
}

// FetchDecision is why a URL may or may not be read.
type FetchDecision struct {
	Allowed bool
	// Reason is operator-facing and states the RULE, not the URL — it goes
	// into the run-transparency record, where a reader needs to know which
	// policy fired rather than re-reading the address they can already see.
	Reason string
}

// MayFetch answers whether the fetch pipeline may read this URL.
//
// It is deliberately conservative about what it cannot parse: a URL this
// function cannot decompose is refused rather than passed through, because
// the failure mode of guessing is fetching something the policy exists to
// keep us away from.
func MayFetch(raw string) FetchDecision {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return FetchDecision{Reason: "the address could not be parsed, so no policy could be applied to it"}
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return FetchDecision{Reason: "only http and https are fetched"}
	}
	host := normalizeHost(u.Hostname())
	for _, denied := range deniedHosts {
		if host == denied || strings.HasSuffix(host, "."+denied) {
			return FetchDecision{
				Reason: "the platform's terms prohibit automated collection; its results are cited, never fetched",
			}
		}
	}
	return FetchDecision{Allowed: true, Reason: "public page, fetched under robots and the site-read caps"}
}

// normalizeHost renders a hostname the way DNS resolves it, so the policy
// cannot be walked past by spelling the same host differently.
//
// The trailing dot is the one that matters and it is not theoretical:
// `linkedin.com.` is the fully-qualified form of `linkedin.com`, resolves to
// the same servers, and is a DIFFERENT string. A suffix check written
// against the raw hostname therefore allows a fetch the deny-list exists to
// refuse — a parser differential between this policy and the HTTP client
// underneath it. Case folds for the same reason.
func normalizeHost(host string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
}

// Citable reports whether a result may be quoted as evidence. Every result
// is: the provider returned it from a public index, and a citation asserts
// only that the claim appears at that address on that date.
//
// It exists as a named function rather than an assumed true so a reader of
// the calling code sees the distinction from MayFetch stated rather than
// implied.
func Citable(Result) bool { return true }
