// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

import (
	"strings"
	"unicode"
)

// signatureStart finds where the signature or legal footer begins, as a rune
// offset, or -1 when nothing below announces itself as boilerplate.
//
// A footer is not a quote. It carries none of the markers quoteStart looks for
// — no '>', no attribution line, no envelope header — so it stays inside the
// candidate reply, and a long English confidentiality notice under a two-line
// German reply puts dozens of English function words in the lead window at the
// same weight as the reply's own. Detect then answers English for a message a
// person wrote in German, which is the defect the whole package exists to
// prevent (DRAFT-AC-E-1).
//
// Two boundaries, because two things are being cut and they announce themselves
// differently. The sig-dash is a convention and is trusted on sight. A legal
// footer has no convention, so it is recognized by the phrases such notices
// actually open with, in the languages this product has correspondence in.
func signatureStart(runes []rune) int {
	if cut := sigDashStart(runes); cut >= 0 {
		return cut
	}
	return footerStart(runes)
}

// sigDashStart finds the RFC 3676 §4.3 sig-dash: a line that is exactly "--",
// optionally with the trailing space the standard asks for.
//
// Anything below it is the signature by the sender's own client's declaration,
// so this needs no heuristic. The line is matched exactly: "-- Lars" is a
// person writing a dash, and "---" is a horizontal rule somebody put in prose.
func sigDashStart(runes []rune) int {
	return firstLineWhere(runes, func(line string) bool {
		return strings.TrimRight(line, " \t") == "--"
	})
}

// footerLeads are the phrases a legal footer opens with. Matched at the start of
// a line, lowercased, because that is where a notice begins — the same words
// mid-sentence ("the information you sent") are prose and must not cut.
//
// The list is short on purpose. Every entry here is a phrase that only appears
// at the head of boilerplate, so a false positive costs a truncated reply while
// a missing entry costs only the footer's votes staying in the pool. Erring
// toward missing one is the cheaper mistake.
var footerLeads = []string{
	// English.
	"this email and any attachments",
	"this e-mail and any attachments",
	"this message and any attachments",
	"this email is confidential",
	"this e-mail is confidential",
	"this message is confidential",
	"the information contained in this",
	"this transmission is intended",
	"if you are not the intended recipient",
	"please consider the environment",
	"disclaimer:",
	"confidentiality notice",
	// German.
	"diese e-mail und etwaige",
	"diese e-mail sowie",
	"diese nachricht und etwaige",
	"diese e-mail enthält vertrauliche",
	"diese nachricht enthält vertrauliche",
	"sollten sie nicht der richtige adressat",
	"wenn sie nicht der richtige adressat",
	"rechtlicher hinweis",
	"vertraulichkeitshinweis",
	"haftungsausschluss",
	// Vietnamese.
	"thư này và bất kỳ",
	"thông tin trong thư này",
	"nếu bạn không phải là người nhận",
}

// footerStart finds the first line that opens a legal footer.
func footerStart(runes []rune) int {
	return firstLineWhere(runes, func(line string) bool {
		lower := strings.ToLower(strings.TrimSpace(line))
		for _, lead := range footerLeads {
			if strings.HasPrefix(lower, lead) {
				return true
			}
		}
		return false
	})
}

// firstLineWhere reports the offset of the first line satisfying match, or -1.
//
// It walks the same way quoteStart does — a line starts after any of the three
// line endings in the wild, and its leading whitespace is skipped — so a footer
// a client indented is still a footer, and the two cuts cannot disagree about
// where a line begins.
func firstLineWhere(runes []rune, match func(string) bool) int {
	for offset, atLineStart := 0, true; offset < len(runes); offset++ {
		if atLineStart && !unicode.IsSpace(runes[offset]) {
			if match(string(lineAt(runes, offset))) {
				return offset
			}
			atLineStart = false
			continue
		}
		atLineStart = atLineStart || runes[offset] == '\n' || runes[offset] == '\r'
	}
	return -1
}
