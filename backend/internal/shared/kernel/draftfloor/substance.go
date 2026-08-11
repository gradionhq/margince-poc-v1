// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftfloor

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Substance is the one sentence of substance a floor draft opens on, in one
// language.
//
// Separate from Phrases because these say something SPECIFIC — about a claim,
// a deal, a commitment, a thread — while Phrases is the skeleton around them.
// Which of these a caller reaches for is the caller's own ranking, and the
// person and account composers deliberately rank them differently: what a
// person SAID outranks the deal it was said about, while on an account a
// commitment we made outranks the deal it belongs to.
//
// The "%s" in each takes the thing being named.
type Substance struct {
	// OpenQuestion: they asked us something and are waiting.
	OpenQuestion string
	// Objection: we owe them an answer.
	Objection string
	// Priority: they told us this matters to them.
	Priority string
	// Commitment: we said we would do something.
	Commitment string
	// Deal: picking up an opportunity already in motion.
	Deal string
	// Thread: following up on an exchange that happened.
	Thread string
}

var substance = map[textlang.Lang]Substance{
	textlang.English: {
		OpenQuestion: "I wanted to come back to you on %s.",
		Objection:    "I still owe you an answer on %s.",
		Priority:     "I know %s matters to you, and I wanted to come back to it.",
		Commitment:   "I wanted to come back to you on %s.",
		Deal:         "I wanted to pick up where we left off on %s.",
		Thread:       "I wanted to follow up on %s.",
	},
	textlang.German: {
		OpenQuestion: "ich wollte auf %s zurückkommen.",
		Objection:    "ich bin Ihnen noch eine Antwort zu %s schuldig.",
		Priority:     "ich weiß, dass Ihnen %s wichtig ist, und wollte das Thema wieder aufgreifen.",
		Commitment:   "ich wollte auf %s zurückkommen.",
		Deal:         "ich wollte da anknüpfen, wo wir bei %s aufgehört haben.",
		Thread:       "ich wollte zu %s nachfassen.",
	},
	textlang.Vietnamese: {
		OpenQuestion: "tôi xin phép trao đổi lại về %s.",
		Objection:    "tôi còn nợ anh/chị câu trả lời về %s.",
		Priority:     "tôi biết %s là điều quan trọng với anh/chị nên xin phép trao đổi lại.",
		Commitment:   "tôi xin phép trao đổi lại về %s.",
		Deal:         "tôi muốn tiếp tục phần chúng ta đang trao đổi về %s.",
		Thread:       "tôi xin phép trao đổi tiếp về %s.",
	},
}

// SubstanceFor resolves the substance lines for a language, falling back the
// same way the skeleton does so one unresolved language never produces two
// languages inside one message.
func SubstanceFor(lang textlang.Lang) Substance {
	lines, ok := substance[langOrDefault(lang)]
	if !ok {
		return substance[DefaultLang]
	}
	return lines
}

// Fill substitutes the single placeholder in a template without going through
// the formatting verbs, so a deal name or subject line containing a percent
// sign renders as itself rather than as a format directive.
func Fill(template, value string) string {
	return strings.Replace(template, "%s", value, 1)
}
