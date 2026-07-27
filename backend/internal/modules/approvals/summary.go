// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The inbox summary is the sentence a human reads before deciding, and it
// is the ONE part of an approval that is prose. Everything else on the row
// is structured and rendered by the UI; the summary is a string some stager
// composed, and several stagers compose it partly out of record text — a
// person's display name, an organization's name — which arrives from
// inbound capture or from a create an agent itself performed.
//
// So it is treated as untrusted at the point it is persisted, not at each
// place it is rendered: a marker that hides the rest of the line, a
// bidirectional override that reverses what follows it, or a benign prefix
// long enough to push the real content out of view are all cheap ways to
// make a human approve one thing while reading another. Sanitizing here
// means no stager can skip it and no renderer has to remember.

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSummaryLen bounds the sentence an inbox row shows. Long enough for an
// operation, a target and the fields that matter; short enough that a
// crafted prefix cannot push them past what a human reads.
const maxSummaryLen = 240

// sanitizeSummary renders a staged summary safe to display: control
// characters and Unicode format marks (bidi overrides and isolates among
// them) are dropped, every run of whitespace collapses to one space, and the
// result is bounded on a rune boundary.
//
// Dropping rather than escaping is deliberate. An escape leaves the operator
// reading a mangled line and still having to reason about it; these
// characters carry no meaning a summary needs, and a name that relies on one
// is a name whose rendering was the point.
func sanitizeSummary(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastWasSpace := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue // invalid encoding: not text, not shown
		case unicode.IsSpace(r):
			if !lastWasSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// Cf covers the bidirectional overrides and isolates
			// (U+202A-E, U+2066-9), the directional marks (U+200E/F,
			// U+061C) and the zero-width joiners a homograph relies on.
			continue
		default:
			b.WriteRune(r)
			lastWasSpace = false
		}
	}
	return boundRunes(strings.TrimSpace(b.String()), maxSummaryLen)
}

// boundRunes truncates to n bytes without splitting a rune, marking that it
// did so — a silently cut summary reads as a complete one.
func boundRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}
