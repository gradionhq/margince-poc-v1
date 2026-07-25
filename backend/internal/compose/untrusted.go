// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The one place untrusted text is made safe to put inside an <untrusted> fence
// — every prompt that fences data calls this, and there is no second spelling.
//
// Every prompt that shows a model captured data wraps it in <untrusted> markers
// and tells the model that what sits between them is DATA, never instructions.
// That promise is only as good as the fence, and the fence is built out of text
// the sender writes: a body containing the closing marker ends the span early,
// and everything after it reads as the prompt's own voice. The sender needs no
// access to try it — an email is enough — and in the counterparty verdict the
// payoff is direct: escape the fence, tell the model to answer "real" with
// confidence 1.0, and a spam address writes itself into the CRM.
//
// The marker is made UNSPELLABLE rather than matched. Recognising it was a
// losing game: the attacker picks from the whole of Unicode, so every list of
// space-like categories missed the next one (Go's \s is ASCII-only, so a
// non-breaking space slipped through; adding the space and format categories
// still let a vertical tab past; adding the control category still let a line
// separator through). And no character class could have caught the two attacks
// that need no exotic characters at all: an invisible rune placed INSIDE the
// word, and a marker spliced across two fields fenced separately — a subject
// ending in "<" and a body beginning "/untrusted>".
//
// A tag needs its opening bracket. Take the bracket away and no spelling of it
// survives: not in another script, not with zero-width filler inside the word,
// and not assembled from two fields, because neither field can contribute the
// one character the tag cannot do without.

import "strings"

// fencedAngle replaces the ASCII "<" in untrusted text. A visible lookalike
// rather than a deletion, because a reader of a captured prompt should be able
// to tell that a sender tried this, and the model should see mangled text rather
// than a boundary.
const fencedAngle = "‹"

// fenceUntrusted makes s safe to place between <untrusted> markers.
//
// Fence the whole untrusted REGION in one call. Fencing field by field does not
// compose: separately-safe pieces can still be concatenated into a marker, which
// is exactly how the subject-plus-body splice worked.
func fenceUntrusted(s string) string {
	return strings.ReplaceAll(s, "<", fencedAngle)
}
