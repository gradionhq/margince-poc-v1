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
// Whichever comes first wins. Trusting the sig-dash unconditionally hid an
// EARLIER footer whenever a company put its legal notice above the signature
// block: the footer's English stayed in the text and outvoted the reply, which
// is the case this function exists to prevent.
func signatureStart(runes []rune) int {
	return earliest(sigDashStart(runes), footerStart(runes))
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
// The list is short on purpose, and every entry is SPECIFIC enough that it can
// only open boilerplate. A generic opener is the trap: "the information
// contained in this" also opens "…proposal explains our position", so the
// entries name the noun ("in this e-mail") and a bare "disclaimer:" is left out
// entirely, because a person writing about a disclaimer starts a line with it.
//
// The asymmetry is why. A false positive TRUNCATES somebody's real reply and
// then answers with the wrong language; a missing entry only leaves that
// footer's votes in the pool, where the lead window usually still carries the
// reply. Missing one is the cheaper mistake, so the list errs that way.
var footerLeads = []string{
	// English.
	"this email and any attachments",
	"this e-mail and any attachments",
	"this message and any attachments",
	"this email is confidential",
	"this e-mail is confidential",
	"this message is confidential",
	"the information contained in this e-mail",
	"the information contained in this email",
	"the information contained in this message",
	"this transmission is intended",
	"if you are not the intended recipient",
	"please consider the environment",
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

// footerMinRunes is how much text has to follow a lead phrase on its own line
// before it is read as boilerplate.
//
// A legal notice is long — the short ones in the wild still run past a hundred
// characters — while somebody who happens to open a line with one of these
// phrases is writing a sentence. The floor costs nothing on a real footer and
// spares a reply that starts with the same words.
const footerMinRunes = 60

// footerStart finds the first line that opens a legal footer.
func footerStart(runes []rune) int {
	return firstLineWhere(runes, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if len([]rune(trimmed)) < footerMinRunes {
			return false
		}
		lower := strings.ToLower(trimmed)
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
