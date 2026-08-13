// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftcheck

// The claims no band makes true.
//
// Every other list in this package is gated on the conversation's state,
// because what it refuses is a claim about the recipient's MEMORY: "as
// discussed" is ordinary in a live exchange and false after eight months. These
// two are not gated, because what they refuse is a claim about the WORLD. A
// call either happened or it did not, and a sentence was either written or it
// was not — no amount of recent correspondence makes an invented one true.

import "github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"

// spokenExchange are the ways a draft asserts that a CONVERSATION happened —
// a call, a meeting, a chat — as opposed to the messages the record holds.
//
// Checked at every band, which is what separates this list from assumedMemory
// above. "As discussed" at band fresh is ordinary: an exchange is running and
// both sides are holding it. "It was a pleasure connecting earlier this week"
// is a different claim entirely — it says two people SPOKE, on a date, and
// nothing in any drafting input can support that. The 360 carries activities;
// a meeting the drafter can see is one on the calendar, and a calendar entry is
// not evidence that it took place or that anyone enjoyed it.
//
// The reported defect that produced this list: a company with a live support
// thread and no call whatsoever was drafted an opener reading "Marine, it was a
// pleasure connecting earlier this week." Every band's list was checked against
// that text and every one of them passed it.
var spokenExchange = map[textlang.Lang][]string{
	textlang.English: {
		"pleasure connecting", "pleasure speaking", "pleasure meeting",
		"pleasure talking", "good to connect", "great to connect",
		"good speaking", "great speaking", "good talking", "great talking",
		"nice to connect", "nice speaking", "enjoyed our",
		"after our call", "on our call", "during our call", "in our call",
		"our conversation earlier", "when we spoke",
	},
	textlang.German: {
		"freute mich", "freut mich, dass wir", "gefreut, sie kennenzulernen",
		"gefreut, dich kennenzulernen", "schön, sie kennenzulernen",
		"nach unserem gespräch", "in unserem gespräch", "unserem telefonat",
		"unser telefonat", "nach unserem call",
	},
	textlang.Vietnamese: {
		"rất vui được trao đổi", "sau cuộc gọi", "trong cuộc gọi",
	},
}

// attributedClaim are the ways a draft puts words in the recipient's mouth.
//
// Also every band, and for the same reason. A drafting surface knows what its
// input said, never who said it: an activity reaches a person or an account
// through links that record what a message CONCERNS rather than who wrote it,
// and no 360 carries participants. The person and account prompts both state
// this ("say 'the question about X' and never 'you wrote'"), which is exactly
// why it needs a check — a rule the prompt states is a rule the model has
// already been observed breaking three times in this program.
//
// The stems rather than whole phrases. Enumerating completions is how the
// earlier lists lost: "introduction by" missed "introduction to", and a German
// list of wellbeing completions missed the one the model actually wrote.
var attributedClaim = map[textlang.Lang][]string{
	textlang.English: {
		"you mentioned", "you said", "you raised", "you told me", "you noted",
		"you indicated", "you expressed", "you described", "you flagged",
		"you brought up", "you pointed out", "as you put it",
	},
	textlang.German: {
		"sie erwähnten", "du erwähntest", "sie sagten", "du sagtest",
		"sie erwähnt haben", "du erwähnt hast", "sie angesprochen haben",
		"du angesprochen hast", "wie sie sagten", "wie du sagtest",
	},
	textlang.Vietnamese: {
		"anh/chị đã đề cập", "anh/chị đã nói",
	},
}
