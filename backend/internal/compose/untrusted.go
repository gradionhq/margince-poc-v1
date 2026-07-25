// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The one place untrusted text is made safe to put inside an <untrusted> fence
// — every prompt that fences data calls this, and there is no second spelling.
//
// Every prompt that shows a model captured data wraps it in <untrusted> markers
// and tells the model that what sits between them is DATA, never instructions.
// That promise is only as good as the fence: text that contains the closing
// marker ends the fence early, and everything the sender wrote after it reads as
// the prompt's own voice.
//
// The sender is the attacker here, and they need no access to exploit it — an
// email body is enough. In the counterparty verdict the payoff is direct: escape
// the fence, tell the model to answer "real" with confidence 1.0, and a spam
// address writes itself into the CRM. The schema validator bounds the damage (a
// forged answer can only carry an id that was actually asked about, so a sender
// cannot vote on anyone else's disposition) but it cannot see a verdict that was
// dictated rather than judged.
//
// So the marker is neutralized in the DATA, not defended against in the prompt:
// a fence a sender cannot close is a fence.

import "regexp"

// untrustedMarker matches any attempt to write an <untrusted> fence marker —
// opening or closing, in any casing, with whitespace anywhere a parser would
// tolerate it. Deliberately broad: the cost of neutralizing a marker that
// appears innocently in someone's mail is a slightly odd-looking prompt, while
// the cost of missing one is a prompt written by the sender.
// The character class is deliberately wider than Go's \s (which is ASCII only):
// a vertical tab, a non-breaking space or a zero-width joiner between the
// bracket and the word is invisible to \s but may still read as a boundary to a
// tokenizer. \p{Zs} covers unicode spaces, \p{Cf} the format/zero-width
// characters an attacker would reach for first.
var untrustedMarker = regexp.MustCompile(`(?i)<[\s\p{Zs}\p{Cf}]*/?[\s\p{Zs}\p{Cf}]*untrusted`)

// fenceUntrusted makes s safe to place between <untrusted> markers by defusing
// any marker it contains. The replacement is visible on purpose — a reader of a
// captured prompt should be able to tell that a sender tried this, and the model
// sees a mangled token rather than a boundary.
func fenceUntrusted(s string) string {
	return untrustedMarker.ReplaceAllString(s, "[removed-marker]")
}
