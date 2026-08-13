// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package textlang guesses what language a piece of correspondence is written
// in, and says so honestly when it cannot tell.
//
// A draft has to be written in the language of the correspondence rather than
// the language of the interface asking for it (DRAFT-AC-E-1): a German thread
// gets a German reply even when the rep's UI is English. Nothing in the
// product knew that, so drafts came out in English on German threads.
//
// The detector is deliberately small and stdlib-only, because it runs in the
// shared tier where the timeline and signals modules can reach it, and because
// the alternative — a model call to find out which language to write in — pays
// for a whole inference to answer a question stopwords answer.
//
// It is biased toward Unknown. This market code-switches constantly: German
// professionals write English mail to German colleagues, quote English threads
// in German replies, and sign off in either. Guessing German on an English
// thread produces a draft the rep cannot send, which is worse than the bug it
// replaces; answering Unknown lets the caller fall back down its own ladder to
// evidence it trusts more. So the winner needs both an absolute floor of hits
// and a clear margin over the runner-up, and gets neither from one greeting.
package textlang

import (
	"strings"
	"unicode"
)

// Lang is a detected correspondence language. Three plus Unknown, because
// these are the languages the product has correspondence in and an
// unrecognized one is more useful named than guessed.
type Lang string

const (
	// Unknown: the evidence did not clear the bar. Never a failure - it is the
	// honest answer for a two-word message, and the caller has other tiers.
	Unknown Lang = ""
	// German.
	German Lang = "de"
	// English.
	English Lang = "en"
	// Vietnamese.
	Vietnamese Lang = "vi"
)

// The bar a winner clears.
const (
	// MinHits is how many stopwords a language needs before its lead means
	// anything. Below this, one shared word ("in", "die" as a Vietnamese
	// syllable) decides the answer.
	MinHits = 3
	// MinMargin is how far ahead of the runner-up the winner must be. A German
	// sentence quoting an English one scores both; only a clear lead is a
	// language rather than a mixture.
	MinMargin = 1.5
	// LeadRunes is how much of the text carries the extra weight when the
	// quoted part does not announce itself. A reply is written above the
	// thread it quotes, so the top of the message is the language being
	// WRITTEN and everything below it is the language being answered; a
	// drafter needs the former.
	LeadRunes = 800
	// LeadWeight is what a hit in that lead is worth against the margin. It
	// only has to overcome an unmarked continuation, since a marked quote is
	// cut off entirely rather than weighted down.
	LeadWeight = 3
	// bodyWeight is what a hit below the lead is worth.
	bodyWeight = 1
)

// germanStopwords and englishStopwords are the discriminating function words
// of each language: articles, pronouns, prepositions, auxiliaries. Words that
// occur in both (in, so, was, hat as a name) are deliberately absent - a
// feature only helps when it separates.
var germanStopwords = map[string]bool{
	"der": true, "die": true, "das": true, "den": true, "dem": true, "des": true,
	"ein": true, "eine": true, "einen": true, "einem": true, "einer": true,
	"und": true, "oder": true, "aber": true, "auch": true, "noch": true,
	"nicht": true, "kein": true, "keine": true, "sehr": true, "schon": true,
	"ich": true, "sie": true, "wir": true, "ihr": true, "mich": true, "mir": true,
	"uns": true, "ihnen": true, "ihre": true, "unser": true, "unsere": true,
	"ist": true, "sind": true, "war": true, "waren": true, "wird": true,
	"werden": true, "haben": true, "hatte": true, "kann": true, "koennen": true,
	"muss": true, "soll": true, "wuerde": true, "gerne": true, "bitte": true,
	"mit": true, "auf": true, "fuer": true, "von": true, "bei": true, "nach": true,
	"aus": true, "ueber": true, "durch": true, "zum": true, "zur": true,
	"dass": true, "wenn": true, "weil": true, "damit": true, "diese": true,
	"dieser": true, "welche": true, "hier": true, "dann": true, "immer": true,
}

var englishStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"not": true, "no": true, "very": true, "already": true, "also": true,
	"i": true, "you": true, "we": true, "they": true, "he": true, "she": true,
	"me": true, "us": true, "them": true, "our": true, "your": true, "their": true,
	"is": true, "are": true, "was": true, "were": true, "will": true, "would": true,
	"have": true, "has": true, "had": true, "can": true, "could": true,
	"should": true, "must": true, "please": true, "thanks": true,
	"with": true, "for": true, "from": true, "about": true, "into": true,
	"that": true, "this": true, "these": true, "those": true, "which": true,
	"when": true, "because": true, "there": true, "then": true, "always": true,
	"just": true, "here": true, "what": true, "how": true,
}

// germanRunes are the letters no English or Vietnamese word carries. One of
// them is worth a stopword hit: they are rarer but far more decisive.
const germanRunes = "äöüßÄÖÜ"

// vietnameseMarkRatio is the share of letters carrying a diacritic above which
// text is Vietnamese. Vietnamese marks a majority of its syllables; German
// umlauts and the odd French loanword in English never approach this.
const vietnameseMarkRatio = 0.12

// Detect reports the language the text is written in, or Unknown when the
// evidence does not clear the bar.
//
// Vietnamese is decided first and separately, by diacritic density rather than
// by stopwords: its function words are short unaccented syllables that collide
// with everything, while its accent pattern is unmistakable.
func Detect(text string) Lang {
	if text == "" {
		return Unknown
	}
	reply, lead := replyText([]rune(text))
	if vietnameseByDiacritics(reply) {
		return Vietnamese
	}

	return winner(scoreStopwords(reply, lead))
}

// score is one language's evidence, counted two ways because the two bars ask
// different questions. Hits asks "is there enough evidence to speak at all",
// so every hit counts once wherever it sits. Weighted asks "which language is
// this written in", where a hit in the lead counts for much more than one in
// the quoted chain below it.
type score struct {
	hits     int
	weighted int
}

// winner applies the two-part bar: an absolute floor on raw hits, then a
// margin on the weighted score. Split out so the thresholds can be exercised
// directly.
func winner(de, en score) Lang {
	high, low, lang := de, en, German
	if en.weighted > de.weighted {
		high, low, lang = en, de, English
	}
	if high.hits < MinHits {
		return Unknown
	}
	if float64(high.weighted) < float64(low.weighted)*MinMargin {
		return Unknown
	}
	return lang
}

// scoreStopwords walks the text once, counting each language's hits and
// weighting those in the lead. A German-only rune counts as a German hit.
func scoreStopwords(runes []rune, lead int) (de, en score) {
	for start := 0; start < len(runes); {
		end := start
		for end < len(runes) && isWordRune(runes[end]) {
			end++
		}
		if end == start {
			if strings.ContainsRune(germanRunes, runes[start]) {
				de.add(weightAt(start, lead))
			}
			start++
			continue
		}

		word := strings.ToLower(string(runes[start:end]))
		weight := weightAt(start, lead)
		switch {
		case strings.ContainsAny(word, germanRunes):
			de.add(weight)
		case germanStopwords[word]:
			de.add(weight)
		case englishStopwords[word]:
			en.add(weight)
		}
		start = end
	}
	return de, en
}

// add records one hit worth the given weight.
func (s *score) add(weight int) {
	s.hits++
	s.weighted += weight
}

// weightAt is what a hit is worth at this offset into the text.
func weightAt(runeOffset, leadEnd int) int {
	if runeOffset < leadEnd {
		return LeadWeight
	}
	return bodyWeight
}

// isWordRune reports whether a rune is part of a word. Letters only: digits and
// punctuation end a word, and an apostrophe inside one ("don't") splits it into
// two harmless fragments rather than a miss.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r)
}

// vietnameseByDiacritics reports whether enough of the text's letters carry a
// diacritic for Vietnamese to be the only explanation.
func vietnameseByDiacritics(text []rune) bool {
	letters, marked := 0, 0
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if isVietnameseMarked(r) {
			marked++
		}
	}
	if letters < MinHits {
		return false
	}
	return float64(marked)/float64(letters) >= vietnameseMarkRatio
}

// vietnameseMarked is the set of accented letters Vietnamese writes, lowercase
// — the six vowels in their five tones, plus đ and the horned/breved forms.
//
// It is an explicit set rather than "any letter that is not plain ASCII",
// because that predicate calls a French loanword Vietnamese: "José's résumé"
// carries three accented letters in twenty-one, which clears the density
// threshold, and so does any Cyrillic or Greek text.
const vietnameseMarked = "àáảãạăằắẳẵặâầấẩẫậ" +
	"èéẻẽẹêềếểễệ" +
	"ìíỉĩị" +
	"òóỏõọôồốổỗộơờớởỡợ" +
	"ùúủũụưừứửữự" +
	"ỳýỷỹỵ" +
	"đ"

// isVietnameseMarked reports whether a rune is one of the accented letters
// Vietnamese writes. Case-folded, so the set above lists each letter once.
//
// Only precomposed (NFC) forms count. Decomposed text spells the same letter
// as a plain ASCII base plus a combining mark, which is a Unicode mark rather
// than a letter and so is never counted — decomposed Vietnamese therefore
// scores zero and falls through to Unknown. That is the honest failure: the
// callers of this package read text out of mail bodies and database columns,
// which arrive precomposed, and normalizing here would pull in a dependency
// this tier does not have.
func isVietnameseMarked(r rune) bool {
	return strings.ContainsRune(vietnameseMarked, unicode.ToLower(r))
}
