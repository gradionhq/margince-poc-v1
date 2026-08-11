// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package draftcheck reads a finished draft and says what is wrong with it,
// deterministically.
//
// It exists because prompt rules keep losing to model reflexes. Three times in
// this program the same thing happened: a rule was written plainly in the
// system prompt, the model read it, and the model did the thing anyway —
// greeting the sender when only the sender was named, opening "I hope you are
// doing well" after eight months of silence, reaching for "circling back" when
// the band forbids it. Adding a fourth sentence to the prompt is the move that
// has already failed.
//
// So the phrases that must not appear are checked in Go, after generation, on
// the text the product is about to serve. A finding is not a refusal: the
// caller decides whether to regenerate, and the deterministic floor is always
// available underneath. What this package guarantees is that nobody has to
// notice the defect by reading the draft.
//
// It is deliberately a SMALL list of phrases with no judgement in it. "Does
// this draft assume shared memory?" is the judge's question and belongs in a
// rubric; "does this draft contain the words 'circling back'" is a fact, and a
// fact is what a gate can be built on.
package draftcheck

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Finding is one thing wrong with a draft, in words a person can act on.
type Finding struct {
	// Rule names what was violated, for the log and the regeneration prompt.
	Rule string
	// Phrase is the text that triggered it, so a reader can find it.
	Phrase string
	// Why says what makes it wrong HERE — the same phrase is fine at another
	// band, which is the whole point of checking against the envelope.
	Why string
}

// assumedMemory are the phrases that gesture at a shared memory instead of
// naming what happened. Harmless in a live exchange, false after a long gap.
var assumedMemory = map[textlang.Lang][]string{
	textlang.English: {
		"circling back", "circle back", "checking in", "check in with you",
		"touching base", "as discussed", "as promised", "as mentioned",
		"our previous discussion", "our previous conversation",
		"our last conversation", "we discussed", "we spoke about",
		"following up on our",
	},
	textlang.German: {
		"wie besprochen", "wie vereinbart", "wie angekündigt", "wie erwähnt",
		"unser letztes gespräch", "unserem letzten gespräch",
		"unsere letzte unterhaltung", "wir hatten besprochen",
	},
	textlang.Vietnamese: {
		"như đã trao đổi", "như đã thống nhất", "như đã đề cập",
	},
}

// wellbeing are the opening pleasantries that announce a template. They are
// filler at every band, and after months of silence they are filler in place of
// the one thing the message owes: a reason for arriving now.
var wellbeing = map[textlang.Lang][]string{
	textlang.English: {
		"hope you are doing well", "hope you're doing well",
		"hope this finds you well", "hope this email finds you well",
		"hope all is well", "hope you are well", "trust you are well",
	},
	textlang.German: {
		"ich hoffe, es geht ihnen gut", "ich hoffe, es geht dir gut",
		"ich hoffe, sie hatten", "ich hoffe, du hattest",
	},
	textlang.Vietnamese: {
		"hy vọng anh/chị vẫn khỏe", "hy vọng mọi việc vẫn tốt",
	},
}

// invention are the claims a first touch reaches for when it has nothing to
// write from: an interest in the recipient's company nobody expressed, a
// familiarity with their work nobody has, a description of what our side sells.
//
// Only checked at band none, and that is the point. "Ich verfolge Ihre Arbeit"
// is a lie in a first message and an ordinary sentence in an established
// relationship, where the correspondence itself is the evidence for it.
var invention = map[textlang.Lang][]string{
	textlang.English: {
		"i have been following", "i've been following", "following your company",
		"following your work", "with great interest", "i noticed that your",
		"i see great potential", "i see good opportunities",
		"our solutions", "our solution", "our products", "we specialize",
		"we specialise", "we help companies", "i help companies",
	},
	textlang.German: {
		"verfolge ich", "verfolge die", "mit interesse", "mit großem interesse",
		"ich beschäftige mich intensiv", "beschäftige mich intensiv",
		"ich sehe hier gute", "sehe gute möglichkeiten", "sehe gute ansätze",
		"unsere lösungen", "unsere lösung", "unsere produkte",
		"wir sind spezialisiert", "ich helfe unternehmen", "wir helfen unternehmen",
	},
	textlang.Vietnamese: {
		"tôi đã theo dõi", "giải pháp của chúng tôi", "chúng tôi chuyên",
	},
}

// Body reads a draft body against the state it was written in.
//
// Nothing is checked at band fresh except the pleasantries: a live exchange may
// legitimately say "as discussed", because the discussion is what both sides
// are still holding. The same words at weeks or months are a claim about the
// recipient's memory that nobody made.
func Body(body string, lang textlang.Lang, band convstate.Band) []Finding {
	lowered := strings.ToLower(body)
	var findings []Finding

	for _, phrase := range wellbeing[lang] {
		if strings.Contains(lowered, phrase) {
			findings = append(findings, Finding{
				Rule:   "wellbeing-opener",
				Phrase: phrase,
				Why:    "an opening pleasantry is filler, and it reads as a template",
			})
		}
	}

	if band == convstate.BandNone {
		for _, phrase := range invention[lang] {
			if strings.Contains(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "invented-first-touch",
					Phrase: phrase,
					Why: "this is a first message and nothing in the input supports that claim — " +
						"write only from the recipient, their employer and the stated reason for writing",
				})
			}
		}
	}

	if band == convstate.BandWeeks || band == convstate.BandMonths {
		for _, phrase := range assumedMemory[lang] {
			if strings.Contains(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "assumed-memory",
					Phrase: phrase,
					Why: "the correspondence has been silent for " + string(band) +
						", so the recipient does not have that exchange in mind — name it instead",
				})
			}
		}
	}
	return findings
}

// Feedback turns findings into the correction a regeneration prompt carries.
// One line per finding, naming the phrase and why it is wrong here, because a
// model told only "try again" produces the same draft with different adjectives.
func Feedback(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe previous draft was rejected. Fix each of these and rewrite it:\n")
	for _, f := range findings {
		b.WriteString("- Remove \"" + f.Phrase + "\": " + f.Why + ".\n")
	}
	return b.String()
}
