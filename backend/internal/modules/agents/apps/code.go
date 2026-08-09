// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// Separating a view's CODE from its commentary, so the sweeps over it can tell
// what the document does from what it says about itself.
//
// WHY THIS EXISTS AT ALL. Every claim these views make — no off-origin reach, no
// markup built from data, no credential — is checked by looking for the
// constructs that would break it. And a comment EXPLAINING one of those
// constructs contains the same characters as the construct: the prose in
// bridge.js had to be reworded once for tripping its own sweep, and the reverse
// is worse. A required call site can be deleted and its explanation left behind,
// and a sweep reading the raw document stays green while the handshake it was
// guarding no longer happens.
//
// So the sweeps read Code(), and the commentary is out of scope for them.
//
// IT ERRS TOWARD KEEPING TEXT, deliberately. This feeds checks that fail on the
// PRESENCE of a forbidden construct, so text wrongly kept is a false positive —
// noisy, immediately visible, fixed by renaming something. Text wrongly removed
// is a forbidden construct nobody sees again. Every ambiguous case below
// therefore resolves to "keep".

import "strings"

// Code answers one asset with its comments removed and everything else — string
// contents included — left exactly as written.
//
// It understands the comment forms these assets actually use: `//` to end of
// line and `/* */`, in JavaScript and CSS alike. It tracks quoted strings so a
// `//` inside one is not read as a comment, which is what stops it from eating
// the rest of a line that merely contained a URL-looking value.
func Code(asset string) string {
	var out strings.Builder
	out.Grow(len(asset))
	// quote is the delimiter of the string being scanned, or 0 outside one.
	var quote byte
	for i := 0; i < len(asset); i++ {
		c := asset[i]
		if quote != 0 {
			out.WriteByte(c)
			switch {
			case c == '\\' && i+1 < len(asset):
				// An escape consumes the next byte whatever it is, so an
				// escaped quote does not end the string.
				i++
				out.WriteByte(asset[i])
			case c == quote:
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(asset) && asset[i+1] == '/' {
			i = skipToLineEnd(asset, i)
			// The newline itself is kept: the checks that read this output are
			// line-oriented in their reporting, and joining two lines could
			// splice two harmless halves into a forbidden spelling.
			out.WriteByte('\n')
			continue
		}
		if c == '/' && i+1 < len(asset) && asset[i+1] == '*' {
			i = skipBlockComment(asset, i)
			out.WriteByte('\n')
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

// skipToLineEnd answers the index of the last byte of a line comment starting at
// start, or the last byte of the asset when the comment runs to the end of it.
func skipToLineEnd(asset string, start int) int {
	if end := strings.IndexByte(asset[start:], '\n'); end >= 0 {
		return start + end
	}
	return len(asset) - 1
}

// skipBlockComment answers the index of the closing `/` of a block comment
// starting at start.
//
// An UNTERMINATED block comment answers start, so the two bytes that opened it
// are the only thing dropped and the rest of the asset is still scanned. That is
// the erring-toward-keeping rule: treating the remainder as commentary would hide
// every construct in it, and an unterminated comment is a syntax error the
// browser reports anyway.
func skipBlockComment(asset string, start int) int {
	if end := strings.Index(asset[start+2:], "*/"); end >= 0 {
		return start + 2 + end + 1
	}
	return start + 1
}
