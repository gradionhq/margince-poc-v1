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
	"unicode"

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
		"our last conversation", "our discussion", "our conversation",
		"we discussed", "we spoke about", "when we last spoke",
		"we last spoke", "following up on our", "picking up where we",
	},
	textlang.German: {
		"wie besprochen", "wie vereinbart", "wie angekündigt", "wie erwähnt",
		"unser letztes gespräch", "unserem letzten gespräch",
		"unsere letzte unterhaltung", "wir hatten besprochen",
		"unserem letzten austausch", "als wir zuletzt", "seinerzeit besprochen",
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
		// "ich hoffe" plus almost anything is the same filler, and enumerating
		// the completions failed on the live stack: the list held "es geht dir
		// gut" and the model wrote "bei dir ist alles gut". The opener is the
		// tell, so the opener is the phrase.
		"ich hoffe", "hoffe, sie hatten", "hoffe, du hattest",
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

// directedRelationship are the ways a draft claims who introduced, referred or
// first contacted whom.
//
// The product holds no person-to-person referral record — referred_by is
// constrained org-to-org — so a directed introduction fact in a draft is
// necessarily read out of quoted correspondence, which is how the reported
// defect got the direction backwards. Silence about introductions is the
// correct behaviour today (DRAFT-AC-E-7), which makes this list a flat refusal
// rather than a judgement about which direction is right.
// The NOUN, not the preposition. A first attempt enumerated "introduction by",
// "introduced by" and the rest; the model wrote "introduction TO" and walked
// straight through. There is no honest use of these words in a chip while the
// product holds no referral record, so the word itself is the refusal and the
// grammar around it does not have to be predicted.
var directedRelationship = map[textlang.Lang][]string{
	textlang.English: {
		// Stems, matched as a word PREFIX, because the word form is not
		// predictable and enumerating it has failed twice on a live stack:
		// "introduction by" missed "introduction to", and the noun list missed
		// "introductory". "introduc" covers introduction/introduced/introducing/
		// introductory; "refer" covers referral/referred/referring.
		"introduc", "intro", "refer",
		"put us in touch", "connected us",
	},
	textlang.German: {
		"vorstell", "vorgestellt", "empfehl", "empfohlen",
		"vermittl", "vermittelt", "in kontakt gebracht",
	},
	textlang.Vietnamese: {
		"giới thiệu", "được giới thiệu",
	},
}

// Reasoning reads the labels a draft shows the rep as its provenance.
//
// A chip is the product explaining itself, and a rep reads it less critically
// than the body they are about to send — so a wrong one is worse there. It gets
// the same phrase lists as the body, plus the directed-relationship refusal,
// which caught "Follow-up to previous introduction by Romina Medici" on a
// thread where the other party made the introduction.
//
// Nothing here is gated on the band: a chip is not prose, and "as discussed" in
// a label is a claim about the record rather than a turn of phrase.
func Reasoning(labels []string, lang textlang.Lang, band convstate.Band) []Finding {
	var findings []Finding
	for _, label := range labels {
		lowered := strings.ToLower(label)
		// EVERY language, not just the draft's. A chip is written for the rep
		// rather than the recipient, and the model reaches for English there
		// even on a German draft — "shared contact introduction" appeared under
		// German prose on a live stack, and a German-only list did not see it.
		for _, phrase := range allDirectedRelationshipPhrases() {
			if startsWord(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "invented-relationship",
					Phrase: phrase,
					Why: "no referral record exists to support who introduced whom, so a " +
						"chip asserting one states a fact the product does not hold",
				})
			}
		}
		findings = append(findings, Body(label, lang, band)...)
	}
	return findings
}

// allDirectedRelationshipPhrases is every language's list at once, for the
// reasoning channel. A chip's own language is not the draft's.
func allDirectedRelationshipPhrases() []string {
	var out []string
	for _, phrases := range directedRelationship {
		out = append(out, phrases...)
	}
	return out
}

// mixedRegister reports a German draft that uses BOTH du and Sie.
//
// To a German reader this is worse than picking the wrong one consistently: it
// reads as machine-written, which is the thing VOICE-STRIP exists to prevent.
// It is checked rather than merely instructed because the prompt already said
// to be consistent and the model was not — three consecutive drafts to one
// person came back du, du, Sie.
//
// The check is on the draft's OWN text, so it needs no envelope: a body holding
// both forms is inconsistent whichever one the envelope asked for.
func mixedRegister(body string) bool {
	return textlang.DetectRegister(body) == textlang.RegisterUnknown &&
		textlang.HasBothRegisters(body)
}

// resolvedEvent are the ways a draft asserts that something the other side was
// WAITING ON has now happened.
//
// The shape that produced this: an input saying "we will look at this again
// once the budget round closes" came back as "as the budget round has now
// concluded". Nobody said it concluded. The draft turned the recipient's own
// condition into a completed fact, and then reasoned from it.
//
// It is a first-person claim about THEIR side's state, which is the one thing a
// drafter cannot know: the record holds what they told us, and anything past
// that is invention wearing the grammar of an update.
var resolvedEvent = map[textlang.Lang][]string{
	textlang.English: {
		"has now concluded", "has now closed", "has now completed",
		"now that the", "now concluded", "now closed", "now complete",
	},
	textlang.German: {
		"inzwischen abgeschlossen", "mittlerweile abgeschlossen",
		"nun abgeschlossen", "jetzt abgeschlossen", "nachdem die",
	},
	textlang.Vietnamese: {
		"đã hoàn tất", "đã kết thúc",
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

	// The wellbeing rule reads the OPENING only. "I hope that works for you" in
	// a closing paragraph is an ordinary sentence; the same words as the first
	// thing a message says are the filler that announces a template.
	for _, phrase := range wellbeing[lang] {
		if contains(opening(lowered), phrase) {
			findings = append(findings, Finding{
				Rule:   "wellbeing-opener",
				Phrase: phrase,
				Why:    "an opening pleasantry is filler, and it reads as a template",
			})
		}
	}

	if lang == textlang.German && mixedRegister(body) {
		findings = append(findings, Finding{
			Rule:   "mixed-register",
			Phrase: "du/Sie",
			Why: "the draft addresses the recipient formally in one sentence and " +
				"familiarly in another — pick the one the correspondence uses and hold it",
		})
	}

	if band == convstate.BandNone {
		for _, phrase := range invention[lang] {
			if contains(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "invented-first-touch",
					Phrase: phrase,
					Why: "this is a first message and nothing in the input supports that claim — " +
						"write only from the recipient, their employer and the stated reason for writing",
				})
			}
		}
	}

	// Only after a long gap. In a live exchange "now that the review is done"
	// usually refers to something the exchange itself established; after months
	// of silence there is no such shared ground, and the draft is asserting a
	// change on the other side's own calendar.
	if band == convstate.BandMonths {
		for _, phrase := range resolvedEvent[lang] {
			if strings.Contains(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "assumed-resolution",
					Phrase: phrase,
					Why: "nothing in the input says that happened — after months of silence " +
						"their side's state is unknown, so ask rather than assert",
				})
			}
		}
	}

	if band == convstate.BandWeeks || band == convstate.BandMonths {
		for _, phrase := range assumedMemory[lang] {
			if contains(lowered, phrase) {
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

// openingRunes is how much of a body counts as its opening, for the rules that
// are only about how a message STARTS. Two or three sentences.
const openingRunes = 240

// opening is the first part of the body, cut on a rune boundary.
func opening(body string) string {
	runes := []rune(body)
	if len(runes) <= openingRunes {
		return body
	}
	return string(runes[:openingRunes])
}

// contains reports whether text holds phrase as whole words.
//
// Plain substring matching false-positives on possessives that overlap: the
// banned "our solution" is inside "your solution", so an honest question about
// the recipient's own system reads as an invented pitch. A phrase must start at
// a word boundary and end at one.
func contains(text, phrase string) bool {
	for offset := 0; ; {
		i := strings.Index(text[offset:], phrase)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(phrase)
		if boundary(text, start-1) && boundary(text, end) {
			return true
		}
		offset = start + 1
		if offset >= len(text) {
			return false
		}
	}
}

// startsWord reports whether text holds phrase beginning at a word boundary,
// with no requirement about where it ENDS.
//
// A stem match: "introduc" has to start a word, so it catches introduction,
// introduced and introductory alike, and does not fire inside an unrelated word
// that merely contains those letters.
//
// A hyphen counts as a boundary, which German needs: the model wrote
// "Intro-Thema" and "Folgekontakt nach Intro" on a live stack, and a stem
// requiring a trailing space saw neither.
func startsWord(text, phrase string) bool {
	for offset := 0; ; {
		i := strings.Index(text[offset:], phrase)
		if i < 0 {
			return false
		}
		start := offset + i
		if boundary(text, start-1) {
			return true
		}
		offset = start + 1
		if offset >= len(text) {
			return false
		}
	}
}

// boundary reports whether the byte at i ends a word (or is off either end).
func boundary(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	r := rune(text[i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
}

// Feedback turns findings into the correction a regeneration prompt carries.
// One line per finding, naming the phrase and why it is wrong here, because a
// model told only "try again" produces the same draft with different adjectives.
func Feedback(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe previous draft was rejected. Rewrite it, and this time:\n")
	for _, f := range findings {
		b.WriteString("- Do not write \"" + f.Phrase + "\" or any synonym of it. " + f.Why + ".\n")
	}
	// A correction that only says what to delete gets the nearest synonym back:
	// told to drop "circling back", the model returns "checking in", which is
	// the same sentence. So the retry is told what to WRITE — a message with a
	// reason to exist does not need a re-contact formula at all.
	b.WriteString("Open on the substance instead. Name what the message is about " +
		"in your own words and ask one question they can answer. A message that " +
		"opens on why you are writing needs no phrase for the act of writing.\n")
	return b.String()
}
