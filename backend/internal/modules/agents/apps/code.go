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
// IT ERRS TOWARD KEEPING TEXT wherever it can see the ambiguity. This feeds
// checks that fail on the PRESENCE of a forbidden construct, so text wrongly
// kept is a false positive — noisy, immediately visible, fixed by renaming
// something. Text wrongly removed is a forbidden construct nobody sees again.
//
// WHAT IT CANNOT SEE, stated because the reassuring version of this paragraph
// would be false. This is a string scanner, not a JavaScript lexer, and three
// constructs desynchronise it in the dangerous direction:
//
//   A REGEX LITERAL containing a quote (`s.replace(/"/g, '')`) opens phantom
//   string state, inverting quote parity for the rest of the asset — after
//   which the INSIDE of a later string is scanned as code and a `//` in it eats
//   the rest of its line, hiding whatever followed.
//
//   A REGEX LITERAL containing `/*` (`s.split(/[/*]/)`) reads as a block-comment
//   opener and swallows everything up to the next `*/` anywhere later.
//
//   A LINE TERMINATOR THE SCANNER DOES NOT KNOW — a lone `\r`, or U+2028/U+2029,
//   both of which end a line comment for a browser — leaves the comment running
//   to the next `\n`, hiding every construct in between. U+2028 is invisible in
//   an editor, which makes it a deliberate-evasion vector rather than a typo.
//
// Rather than pretend to lex JavaScript, AssumptionsHold refuses an asset that
// could contain any of them, and appsfitness_test.go fails the build on it. That
// turns a silent hole into a loud one at the moment an asset first needs a
// construct this cannot read.
//
// For the regex cases the refusal is a POSITIVE rule about slashes rather than a
// residue check on the scan, and the difference matters: a scan that desyncs on
// one regex quote and resyncs on a second ends in balanced quote state while the
// text between them was read in the wrong mode, so "ended balanced" is not proof
// of anything. What IS checkable without lexing is that a script contains no bare
// slash at all outside a string — no regex, no division — which these assets do
// not need.

import (
	"strconv"
	"strings"
)

// Code answers one asset with its comments removed and everything else — string
// contents included — left exactly as written.
//
// It understands the comment forms these assets actually use: `//` to end of
// line and `/* */`, in JavaScript and CSS alike. It tracks quoted strings so a
// `//` inside one is not read as a comment, which is what stops it from eating
// the rest of a line that merely contained a URL-looking value.
func Code(asset string) string {
	code, _ := scan(asset)
	return code
}

// scanned is what one pass over an asset learned besides the stripped text.
type scanned struct {
	// quote is the string delimiter still in force at the end, 0 when balanced.
	quote byte
	// bareSlash is true when a `/` was reached in CODE position — outside any
	// string, and not opening or closing a comment. In JavaScript that is a
	// regex literal or a division, and this scanner can tell neither from the
	// other; a regex is what desynchronises it.
	bareSlash bool
}

// scan is Code's body, answering what it learned as well as the stripped text.
func scan(asset string) (string, scanned) {
	var out strings.Builder
	out.Grow(len(asset))
	// quote is the delimiter of the string being scanned, or 0 outside one.
	var quote byte
	var learned scanned
	for i := 0; i < len(asset); i++ {
		if quote != 0 {
			i, quote = copyStringByte(&out, asset, i, quote)
			continue
		}
		switch c := asset[i]; {
		case c == '\'' || c == '"' || c == '`':
			quote = c
			out.WriteByte(c)
		case opensComment(asset, i, '/'):
			i = skipToLineEnd(asset, i)
			// The newline itself is kept: the checks that read this output are
			// line-oriented in their reporting, and joining two lines could
			// splice two harmless halves into a forbidden spelling.
			out.WriteByte('\n')
		case opensComment(asset, i, '*'):
			i = skipBlockComment(asset, i)
			out.WriteByte('\n')
		default:
			if c == '/' {
				learned.bareSlash = true
			}
			out.WriteByte(c)
		}
	}
	learned.quote = quote
	return out.String(), learned
}

// opensComment reports whether a comment of the given second byte — `/` for a
// line comment, `*` for a block one — starts at i.
func opensComment(asset string, i int, second byte) bool {
	return asset[i] == '/' && i+1 < len(asset) && asset[i+1] == second
}

// copyStringByte copies one byte from inside a string literal, and answers the
// index it consumed to and the delimiter still in force (0 once it closed).
//
// It exists so Code's loop reads as the four cases it has, rather than carrying
// the escape handling inline where it doubles the branching of the whole scan.
func copyStringByte(out *strings.Builder, asset string, i int, quote byte) (int, byte) {
	c := asset[i]
	out.WriteByte(c)
	// An escape consumes the next byte whatever it is, so an escaped quote does
	// not end the string.
	if c == '\\' && i+1 < len(asset) {
		i++
		out.WriteByte(asset[i])
		return i, quote
	}
	if c == quote {
		return i, 0
	}
	return i, quote
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

// unlexable are the constructs Code cannot scan past safely, each with the
// reason it matters. See the package-level note above for what each one does to
// the scan.
var unlexable = []struct {
	Token string
	Why   string
}{
	{"\r", "a lone carriage return ends a line comment for a browser but not for this scanner, so the comment would run on and hide what follows"},
	{"\u2028", "U+2028 ends a line comment for a browser but not for this scanner, and it is invisible in an editor"},
	{"\u2029", "U+2029 ends a line comment for a browser but not for this scanner, and it is invisible in an editor"},
}

// AssumptionsHold reports why Code cannot be trusted on this asset, or "" when
// it can.
//
// It is a POSITIVE check on the input rather than a cleverer scanner: the
// alternative is a JavaScript lexer, and a half-written one is worse than a
// scanner whose limits are enforced. An asset that trips this is not
// necessarily unsafe — it is unreadable to the sweeps that depend on Code, which
// is the same thing from the gate's point of view.
func AssumptionsHold(name, asset string) string {
	for _, u := range unlexable {
		if strings.Contains(asset, u.Token) {
			return "contains " + strconv.QuoteToASCII(u.Token) + ": " + u.Why
		}
	}
	_, learned := scan(asset)
	// A SCRIPT may not carry a bare slash. A regex literal is what desyncs this
	// scanner, and a regex cannot be told from a division without parsing — so
	// neither is allowed, which is a rule about these four assets rather than
	// about JavaScript, and one they already keep.
	//
	// Stylesheets are exempt: CSS has no regex literals and no line comments, and
	// a bare slash there is ordinary shorthand (`font: 14px/1.5`).
	if strings.HasSuffix(name, ".js") && learned.bareSlash {
		return "contains a `/` outside a string that does not open or close a comment. In a script that is a regex " +
			"literal or a division, and this scanner cannot tell them apart — a regex carrying a quote or a `/*` " +
			"desynchronises it and hides whatever follows. Rewrite it without the slash, or teach code.go to lex"
	}
	// A second net, and NOT a proof: unbalanced quote state means a string was
	// opened that the scan never saw close, so everything after it was read in
	// the wrong mode. Balanced state proves nothing — two desyncs cancel — which
	// is why the slash rule above is the one doing the work.
	if learned.quote != 0 {
		return "leaves an unclosed " + strconv.QuoteRune(rune(learned.quote)) +
			" string when scanned, so everything after it was read in the wrong mode"
	}
	return ""
}
