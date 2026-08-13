// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

// Where the message being WRITTEN ends and the machinery around it begins:
// the quote markers, the attribution lines, and the floors that stop a cut
// from leaving nothing to read.
//
// It sits beside textlang.go because that file answers "which language is
// this" and this one answers "which part of it is the reply" — two questions
// with one caller, and the second is the one that keeps being got wrong.

import (
	"strings"
	"unicode"
)

// quoteMarkers are the ways a mail client announces that what follows is the
// thread being replied to rather than the reply. Ordinary correspondence does
// not produce these lines by accident.
var quoteMarkers = []string{
	">",
	"-----Original Message",
	"-----Ursprüngliche Nachricht",
	"________________________________",
	"Von: ",
	"From: ",
}

// attributionOpeners begin the "On <date>, <name> wrote:" line that clients
// put above a quoted thread. They are ordinary words as well — "On balance",
// "Am Montag besprechen wir" — so an opener alone is not a marker; the line
// must also carry the verb that makes it an attribution.
var attributionOpeners = []string{"On ", "Am "}

// attributionVerbs are how such a line says somebody wrote something. One of
// these must appear on the same line as the opener for it to be a quote header.
var attributionVerbs = []string{"wrote:", "schrieb:", "schrieb ", " wrote "}

// attributionMaxRunes bounds how far along a line the verb is looked for. An
// attribution line is one date and one name long; a paragraph that happens to
// open with "On" and mention writing much later is prose.
const attributionMaxRunes = 200

// ownHeaderRunes is how far into the text a "From:"/"Von:" line may sit and
// still belong to THIS message rather than to a quoted one.
//
// A captured email body often begins with its own envelope headers — the
// capture path stores "From: …\nTo: …\n\n" above the text somebody wrote. Those
// same words further down the message introduce the thread being quoted. The
// difference is position: a header block is at the top, and a quoted-message
// header comes after the reply. Generous enough for a few header lines with
// long addresses, far short of any real reply.
const ownHeaderRunes = 400

// replyText narrows the text to the message actually being written, and says
// how much of what remains is the lead.
//
// Where a quote marker announces the thread below, everything from there down
// is dropped: it is a different message, in whatever language its author chose,
// and counting it as diluted evidence still lets a long English chain outvote
// the short German reply on top of it. A reply with no marker keeps its whole
// text and leans on the LeadRunes window instead.
//
// A signature or legal footer is dropped the same way and for the same reason.
// It carries no quote marker, so it survives the cut above and its boilerplate
// lands in the lead window: a long English confidentiality notice under a short
// German reply can outvote the reply itself.
//
// Both cuts share one floor. A message that is ONLY quoted text, or only a
// footer, keeps it, because then that text is all the evidence there is and
// refusing to read it would answer Unknown for a forward whose language is
// perfectly clear.
func replyText(runes []rune) (text []rune, lead int) {
	runes = cutAt(runes, earliest(quoteStart(runes), signatureStart(runes)))
	return runes, min(len(runes), LeadRunes)
}

// earliest picks the first boundary any of the detectors found, ignoring the
// ones that found nothing.
//
// ONE cut, at the earliest boundary — not one cut per detector. Cutting
// sequentially looks equivalent and is not, in both directions:
//
//   - The second cut measures the floor against text the first cut already
//     shortened. A short reply plus a long signature clears twelve words, so
//     the quote goes; the signature cut then sees only the short reply, falls
//     under the floor, and leaves the English signature in. The reply loses to
//     its own signature, which is the defect this file exists to fix.
//   - Whichever detector runs first wins, even when it found a LATER boundary.
//     A footer at rune 84 followed by a sig-dash at 264 kept the footer.
//
// Both disappear once the boundaries are compared before anything is cut and
// the floor is measured once, against the text as it arrived.
//
// Note for whoever changes this next: saysSomething below now also rescues the
// first case, so reverting to sequential cuts does not immediately break the
// suite. That makes this the cheaper of two guards against the same defect, not
// a redundant one — the second cut would go back to asking its floor a question
// about the wrong text, and the next short reply whose stopwords fall under the
// bar would lose to its own signature again.
func earliest(offsets ...int) int {
	found := -1
	for _, offset := range offsets {
		if offset >= 0 && (found < 0 || offset < found) {
			found = offset
		}
	}
	return found
}

// cutAt drops everything from the offset down, unless doing so would leave too
// little text to read.
//
// The floor is what keeps the cut honest. A message that is ONLY a quote, or
// only a footer, keeps it: then that text is all the evidence there is, and
// refusing to read it answers Unknown for a forward whose language is perfectly
// clear. The quote cut learned this the expensive way — twice, from a user
// report. A negative offset means nothing announced itself, so nothing is cut.
func cutAt(runes []rune, offset int) []rune {
	if offset <= 0 {
		return runes
	}
	kept := runes[:offset]
	if WordsWritten(string(kept)) >= minReplyWords || saysSomething(kept) {
		return kept
	}
	return runes
}

// saysSomething reports whether this much text scores as a language on its own.
//
// The word floor asks how MUCH was written, and that is the wrong question for
// a short reply. "Hallo Marek, danke dir, ich schaue es mir an" is nine words —
// under the floor — and unmistakably German, so refusing to cut left an English
// signature below it in the pool, where it outvoted the reply.
//
// Two things keep it from becoming a way around the floor rather than a
// refinement of it.
//
// It reads only the lines somebody WROTE. Scoring the raw text let a bare
// "Subject: Update on the plan and the budget" clear the stopword bar on its
// own "the"s while counting zero authored words — a metadata line authorizing
// the removal of the whole quoted thread below it, which is exactly the defect
// the floor exists to prevent.
//
// And it still wants a sentence's worth of words. The Vietnamese path is
// decided by the share of accented runes, so a single word like "Résumé" reads
// as Vietnamese on two of its six letters; a lone word is not a reply in any
// language, whatever it is written in.
func saysSomething(kept []rune) bool {
	written := []rune(strings.Join(WrittenLines(string(kept)), "\n"))
	if WordsWritten(string(written)) < minSpokenWords {
		return false
	}
	de, en := scoreStopwords(written, len(written))
	return winner(de, en) != Unknown || vietnameseByDiacritics(written)
}

// minSpokenWords is the shortest run of words that can be a reply at all.
//
// Well under minReplyWords, because the whole point here is to read a curt
// answer the word floor rejects ("Danke dir, das schaue ich mir an und melde
// mich" is ten). It is a floor against fragments, not against brevity: below
// this there is no sentence, only a word or two that happen to score.
const minSpokenWords = 5

// minReplyWords is how much text has to sit above a quote before that text is
// treated as the reply.
//
// A forwarded message is stored as its envelope headers and then the whole
// original, every line quoted. Cutting at the quote leaves the address lines
// and nothing else - 53 runes of a 1180-rune German mail, no words to read, and
// the language resolves to Unknown so the draft comes out in English. That is a
// real defect a user reported twice.
//
// Twelve words, because the text above a quote is not only the reply. A stored
// activity carries its SUBJECT line above the envelope headers, so a forwarded
// German mail arrived with "Update zu Margince 17.7.2026" plus two addresses —
// enough to clear a three-word floor, and the 5,400 runes of German below it
// were cut away. A subject plus a header pair is roughly eight words; a real
// reply, even a curt one, is a sentence and clears twelve.
const minReplyWords = 12

// quoteStart finds where the quoted thread begins, as a rune offset, or -1
// when no line announces itself as quoted.
//
// A line starts after any of the three line endings in the wild, and its
// leading whitespace is skipped: "  > quoted" is a quote however the client
// indented it.
func quoteStart(runes []rune) int {
	for offset, atLineStart := 0, true; offset < len(runes); offset++ {
		if atLineStart && !unicode.IsSpace(runes[offset]) {
			line := lineAt(runes, offset)
			if startsQuote(line) && !isOwnHeader(line, offset) {
				return offset
			}
			atLineStart = false
			continue
		}
		atLineStart = atLineStart || runes[offset] == '\n' || runes[offset] == '\r'
	}
	return -1
}

// isOwnHeader reports whether an address header at this offset belongs to the
// message itself rather than announcing a quoted one.
//
// Without this a captured email is cut to nothing: the capture path stores the
// envelope headers above the body, so the text starts "From: …", the whole
// message reads as quoted, and the language of a German thread resolves to
// Unknown — which is exactly how a German thread produced an English draft.
func isOwnHeader(line []rune, offset int) bool {
	if offset >= ownHeaderRunes {
		return false
	}
	return IsMailHeader(string(line))
}

// lineAt returns the rest of the line beginning at offset.
func lineAt(runes []rune, offset int) []rune {
	for end := offset; end < len(runes); end++ {
		if runes[end] == '\n' || runes[end] == '\r' {
			return runes[offset:end]
		}
	}
	return runes[offset:]
}

// startsQuote reports whether this line announces quoted text.
func startsQuote(line []rune) bool {
	text := string(line)
	for _, marker := range quoteMarkers {
		if strings.HasPrefix(text, marker) {
			return true
		}
	}
	return isAttributionLine(text)
}

// authoredText is the text above the first quoted or forwarded line — what the
// sender wrote themselves, with everything they merely carried along removed.
//
// It differs from NewTextOnly in exactly one way, and that difference is its
// whole reason to exist: a boundary at the very first line cuts to nothing here
// rather than keeping the quote. NewTextOnly's caller asks what language a text
// is in, where a quote is evidence worth reading; this one's caller asks what
// our correspondent said that a draft may stand on, where a message that added
// nothing of its own has no answer to give.
//
// It reuses startsQuote and signatureStart rather than scanning for '>' so a
// marker this file learns about reaches every reading at once.
func authoredText(text string) string {
	runes := []rune(text)
	offset := earliest(quoteStart(runes), signatureStart(runes))
	if offset < 0 {
		return text
	}
	return string(runes[:offset])
}

// isAttributionLine reports whether the line is a client's "On <date>, <name>
// wrote:" header rather than a sentence that happens to begin the same way.
func isAttributionLine(line string) bool {
	opens := false
	for _, opener := range attributionOpeners {
		if strings.HasPrefix(line, opener) {
			opens = true
			break
		}
	}
	if !opens {
		return false
	}

	head := line
	if runes := []rune(line); len(runes) > attributionMaxRunes {
		head = string(runes[:attributionMaxRunes])
	}
	for _, verb := range attributionVerbs {
		if strings.Contains(head, verb) {
			return true
		}
	}
	return false
}

// NewTextOnly returns only what the author of THIS message wrote: everything
// above the quote marker and above the signature, with no floor.
//
// It differs from the language detector's cut in exactly that missing floor.
// Detection keeps a message that is only quoted text, because then the quote is
// the only evidence of language there is and answering Unknown would be worse.
// A caller asking "what did this person say" must not: the whole point is that
// text below the marker was written by somebody ELSE, and a reader who cannot
// tell the difference can be fed words through a quoted chain. Capture's
// correspondence gate reads a reply for its intent, so it takes this one.
//
// It also cuts STRICTLY, where cutAt keeps the quote when too little was
// written above it. That fallback is right for language detection and wrong
// here for the same reason: a one-line "Not interested." is under every floor
// cutAt applies, so keeping the quote would hand the reader the sender's own
// words as if the replier had written them.
//
// An empty result is the honest answer for a message that added nothing.
func NewTextOnly(text string) string {
	runes := []rune(text)
	if offset := earliest(quoteStart(runes), signatureStart(runes)); offset > 0 {
		runes = runes[:offset]
	}
	return strings.TrimSpace(string(runes))
}
