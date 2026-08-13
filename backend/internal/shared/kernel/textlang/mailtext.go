// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

// What in a stored mail body is somebody writing, and what is plumbing.
//
// This is one question with one answer, and it had been answered in two places
// with different lists: the language detector knew about "From:" and "Von:",
// the draft's snippet knew about six headers, and neither knew what the other
// knew. Four separate defects came out of that seam, all with the same shape —
// a German thread drafting in English because the words never reached the
// detector.
//
// So the vocabulary lives here, once, beside the detector that needs it most.

import (
	"strings"
	"unicode"
)

// mailHeaders are the envelope lines the capture path stores above a body.
// Both languages, because a German mail client writes German headers.
var mailHeaders = []string{
	"From: ", "To: ", "Cc: ", "Bcc: ", "Subject: ", "Date: ", "Sent: ",
	"Von: ", "An: ", "Kopie: ", "Betreff: ", "Gesendet: ", "Datum: ",
}

// IsMailHeader reports whether a line is one of those envelope lines.
//
// Exported because the drafting surfaces strip the same block before showing a
// message to a model, and a second list there is how the two answers drifted
// apart in the first place.
func IsMailHeader(line string) bool {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	for _, header := range mailHeaders {
		if strings.HasPrefix(trimmed, header) {
			return true
		}
	}
	return false
}

// MessageOpening is the start of what somebody wrote in a stored body, bounded
// to maxRunes: the envelope block dropped, the quoted thread and signature cut,
// and the remainder trimmed on a rune boundary so a multi-byte word is never
// sliced in half.
//
// The opening rather than the whole message. An email states why it was sent in
// its first lines and spends the rest on detail, and the detail is what a reply
// asks about rather than repeats back — while every rune of it is prompt cost on
// every draft.
//
// An empty result is honest: a mail that is only headers, or only a quoted
// thread, carries no words for a draft to be grounded in, and returning the
// addresses instead would put them in the prompt.
//
// The quoted-only case needs its own answer here, and the reason is worth
// stating. NewTextOnly keeps a message that is ENTIRELY a quote, because its
// caller asks what language this text is in and the quote is then the only
// evidence there is. Grounding asks the opposite question — what did our
// correspondent write that a draft may stand on — and the honest answer for a
// message that added nothing of its own is nothing.
//
// That case is not only the ">"-prefixed one. A forwarded mail announces itself
// with "-----Original Message-----" and then carries plain unprefixed headers
// and prose: nobody on this thread wrote a word of it, and letting it through
// would ground a draft in a third party's text and put their address in the
// prompt. So the cut is made at the first quote marker wherever it falls,
// including the very first line.
//
// maxRunes must be positive. A caller asking for a bounded opening and getting
// the whole message back is the failure this bound exists to prevent, so a
// non-positive bound yields nothing rather than everything.
func MessageOpening(body string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	text := strings.TrimSpace(authoredText(stripMailHeaders(body)))
	runes := []rune(text)
	if len(runes) > maxRunes {
		return strings.TrimSpace(string(runes[:maxRunes]))
	}
	return text
}

// stripMailHeaders drops the leading From:/To: block the capture path writes
// above a stored body.
//
// It requires the block to open on a header line and to be closed by a blank
// line, which is the shape capture really writes. Any leading run of
// header-shaped lines would eat real prose: "From: our finance team's
// perspective" over "To: make this work we need..." is a sentence, not an
// envelope.
func stripMailHeaders(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || !IsMailHeader(strings.TrimSpace(lines[0])) {
		return body
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if IsMailHeader(trimmed) {
			continue
		}
		if trimmed == "" {
			// The blank line that closes a header block: everything after it is
			// the message.
			return strings.Join(lines[i+1:], "\n")
		}
		// A non-header, non-blank line inside the run means this was never a
		// header block. Keep the whole thing rather than guess where it ends.
		return body
	}
	// Every line was a header and no blank line ever closed the block. That is
	// what capture writes for a mail with no body at all — the folded
	// "From: …\nTo: …" and nothing after it (mailmap.Message.ToRecord) — so the
	// message is its addresses and there is nothing anybody wrote.
	//
	// It is ALSO what a paragraph opening "From: our finance team's perspective"
	// over "To: make this work we need…" looks like, and eating that would drop
	// a real message. The two are told apart by what follows the colon: capture
	// writes an address there, and prose writes words. Only the address shape is
	// dropped.
	if headerBlockIsAddressesOnly(lines) {
		return ""
	}
	return body
}

// headerBlockIsAddressesOnly reports whether every line of a closed-less header
// run carries an address rather than prose, which is what separates the
// envelope capture stores for a body-less mail from a sentence that happens to
// begin "From: …".
func headerBlockIsAddressesOnly(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		_, value, found := strings.Cut(trimmed, ":")
		if !found || !looksLikeAddressField(strings.TrimSpace(value)) {
			return false
		}
	}
	return true
}

// looksLikeAddressField reports whether a header's value is the address (or the
// "-" capture writes when it has none) rather than a sentence.
//
// An address carries no spaces; a sentence carries several. Anything with a
// space that is not a bare "-" reads as prose, which keeps the strip narrow:
// dropping a real paragraph is the worse failure of the two.
func looksLikeAddressField(value string) bool {
	if value == "" || value == "-" {
		return true
	}
	return !strings.ContainsFunc(value, unicode.IsSpace)
}

// WordsWritten counts the words in text that somebody actually wrote, ignoring
// envelope headers and quoted lines.
//
// It answers the question every caller here really has: is there a message in
// this, or only the machinery around one? A forwarded mail is stored as its
// headers and then the original with every line quoted — plenty of characters,
// no words of its own — and treating that as a reply is what left a 1180-rune
// German mail with 53 runes of addresses and an English draft.
func WordsWritten(text string) int {
	words := 0
	for _, line := range WrittenLines(text) {
		inWord := false
		for _, r := range line {
			if isWordRune(r) {
				if !inWord {
					words++
					inWord = true
				}
				continue
			}
			inWord = false
		}
	}
	return words
}

// WrittenLines is text with the envelope headers and quoted lines dropped: the
// lines somebody actually typed.
//
// It exists so that every question about "is there a message here" is asked of
// the SAME text. Counting words after stripping headers while scoring stopwords
// before stripping them let a bare "Subject: Update on the plan and the budget"
// answer zero words and still clear the stopword bar on its own "the"s — which
// is how a metadata line came to authorize cutting a whole quoted thread away.
func WrittenLines(text string) []string {
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	written := lines[:0]
	for _, line := range lines {
		if IsMailHeader(line) || startsQuote([]rune(strings.TrimLeftFunc(line, unicode.IsSpace))) {
			continue
		}
		written = append(written, line)
	}
	return written
}
