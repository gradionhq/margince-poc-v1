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
