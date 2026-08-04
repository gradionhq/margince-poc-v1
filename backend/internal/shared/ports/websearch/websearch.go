// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package websearch defines the governed web-search seam (ADR-0081 / A126).
//
// The product already reads a URL it was handed: the site-read pipeline is
// robots-aware, paced, capped, and feeds an evidence-or-omit extractor. What
// it cannot do is FIND a page nobody named. Every capability that needs
// discovery — the deep-person research profiler, account research, any public
// signal lane — waits on that, which is why ADR-0081 made the seam a
// pre-build gate rather than an implementation detail.
//
// Provider choice is config, not architecture, exactly as ports/model treats
// the LLM: adapters live behind this interface and the operator binds one. A
// deployment that binds none simply has no search, and the features above
// degrade to their captured-data floor honestly rather than silently
// stubbing.
//
// THE POSTURE THIS SEAM EXISTS TO HOLD (ADR-0081 §3):
//
//   - Search may RETURN anything; what the product FETCHES is policy. A
//     result's own metadata — title, snippet, URL, date — is usable evidence
//     without ever touching the target site, which is how a public
//     professional headline reaches a record without a fetch.
//   - Page fetches ride the existing site-read pipeline and inherit its
//     robots handling, identifying user agent, pacing and caps.
//   - Auth-walled platforms are never fetched. No credentialed fetching, no
//     paywall circumvention, no bot-detection evasion, in any release.
//
// A Result is EVIDENCE, not a fact: the provider is named as its source, and
// a snippet quoted from one carries the URL and the read date exactly as a
// site-read claim carries its page. Article 14 notice is answerable by
// enumeration because of that, not by archaeology.
package websearch

import (
	"context"
	"errors"
	"time"
)

// ErrNoProvider reports a deployment that bound no search provider. It is a
// capability limit, not a domain sentinel: callers degrade to what they can
// answer from captured data and say so, rather than presenting an empty
// result set as "nothing exists about this person".
var ErrNoProvider = errors.New("websearch: no provider is configured for this deployment")

// ErrBudgetExhausted reports the workspace's search allowance spent for the
// window. Background work defers to the next one; interactive work degrades
// with an honest notice. It mirrors the model runtime's budget posture so an
// operator reasons about one spend model rather than two.
var ErrBudgetExhausted = errors.New("websearch: the workspace search budget for this window is spent")

// Result is one hit as the provider asserts it. Every field is the
// provider's claim except RetrievedAt, which is ours.
type Result struct {
	// Title and Snippet are the provider's rendering of the page. They are
	// quotable as evidence WITHOUT fetching URL — that is the whole point of
	// carrying them: the two facts a record most often needs (a role, an
	// employer) are frequently right here, and reading them costs the target
	// site nothing.
	Title   string
	Snippet string

	// URL is where the claim lives. Whether it may be FETCHED is the source
	// policy's decision, not this type's — a URL on the deny-list is still a
	// perfectly good citation.
	URL string

	// PublishedAt is the provider's date for the page when it offers one.
	// Nil means unknown, which is different from old.
	PublishedAt *time.Time

	// RetrievedAt is when WE read this result. It is what makes a stored
	// claim age visibly instead of pretending to be current, so it is
	// stamped by the adapter rather than taken from the provider.
	RetrievedAt time.Time
}

// Query is one search. Kept deliberately small: this seam finds pages, and
// every knob that shapes what happens to them afterwards belongs to the
// caller's own policy rather than to the provider contract.
type Query struct {
	// Terms is the search string.
	Terms string

	// MaxResults bounds what the adapter returns. Zero takes the adapter's
	// own conservative default — an unbounded search is a cost with no
	// ceiling, and the caller usually wants the first few.
	MaxResults int

	// Site optionally restricts the search to one domain. It narrows a query
	// to a company's own pages, which is the cheapest and least intrusive
	// way to ask most questions this product asks.
	Site string
}

// Client is the swappable search interface; selection is config.
//
// Search reports ErrNoProvider when the deployment bound none, and
// ErrBudgetExhausted when the workspace's allowance is spent. Both are
// capability answers a caller degrades on, never failures to swallow.
type Client interface {
	Search(ctx context.Context, q Query) ([]Result, error)

	// Provider names the bound adapter for the run-transparency record.
	// Every stored claim can then say which index it came from, which is
	// half of what the profiler's "Sources read" rail displays.
	Provider() string
}
