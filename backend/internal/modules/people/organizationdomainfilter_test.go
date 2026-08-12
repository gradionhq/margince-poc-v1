// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

// A caller's domain filter is reduced to the host the column stores.
//
// The filter narrows against `organization_domain.domain`, which holds a bare
// folded host, and the operand does not: a person filtering by domain is
// pasting one out of a signature or an address bar, so the scheme, the `www.`,
// the path and the case all arrive attached. Every spelling below has to reach
// the same row, or the filter answers "no account lists this" about an account
// that lists it — a confident wrong answer, which is the one thing a narrowing
// filter must not produce.
//
// It is worth pinning HERE rather than trusting values.ParseDomain's own tests.
// Those prove the parse; this proves the filter USES it, which is the half that
// goes missing when a clause is written against the raw operand. That is not
// hypothetical for this path: the write side reduces its domains through
// parseOrgDomains, and the two only agree because both call the same parse.
func TestTheDomainFilterMatchesTheHostHoweverItWasSpelled(t *testing.T) {
	const host = "acme.example"
	for _, spelling := range []string{
		"acme.example",
		"ACME.example",
		"www.acme.example",
		"https://acme.example",
		"https://www.acme.example/careers",
		"http://www.ACME.example/careers?ref=x",
		// A trailing root label is the same host; the parse drops it.
		"www.acme.example.",
	} {
		t.Run(spelling, func(t *testing.T) {
			if got := foldDomainQuery(spelling); got != host {
				t.Errorf("foldDomainQuery(%q) = %q, want %q — this spelling reaches a "+
					"different row than the same account written plainly", spelling, got, host)
			}
		})
	}
}

// An operand naming no host at all folds to a value no row carries.
//
// It does NOT become an error: the caller named an account by something, and
// the honest answer is a page with nothing in it rather than a refusal. The
// value must still be unmatchable — folding to the empty string is what makes
// the EXISTS clause select nothing, and folding to anything a row could hold
// would quietly widen the page instead.
func TestADomainFilterThatNamesNoHostMatchesNothing(t *testing.T) {
	for _, spelling := range []string{"", ".", "https://", "https:///path", "not a domain"} {
		t.Run(spelling, func(t *testing.T) {
			if got := foldDomainQuery(spelling); got != "" {
				t.Errorf("foldDomainQuery(%q) = %q, want an unmatchable value — a filter that "+
					"names no host must select nothing rather than something", spelling, got)
			}
		})
	}
}
