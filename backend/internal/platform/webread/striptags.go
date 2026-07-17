// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import "strings"

// StripTags reduces HTML to whitespace-normalized text. Deliberately crude:
// evidence snippets are matched against THIS text, so the same reduction
// defines both what the model sees and what counts as verbatim — any change
// here silently invalidates stored evidence, treat the output as a contract.
func StripTags(html string) string {
	var b strings.Builder
	inTag, inScript := false, false
	for i, r := range html {
		switch {
		case inScript:
			if r == '<' && (foldPrefix(html[i:], "</script") || foldPrefix(html[i:], "</style")) {
				inScript, inTag = false, true
			}
		case r == '<':
			if foldPrefix(html[i:], "<script") || foldPrefix(html[i:], "<style") {
				inScript = true
			} else {
				inTag = true
			}
		case r == '>':
			inTag = false
			b.WriteRune(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// foldPrefix is an ASCII case-insensitive prefix test on the ORIGINAL bytes.
// Lowercasing the whole document first is not an option: Unicode case mapping
// changes byte lengths (U+212A → "k"), so indexes into a lowered copy drift
// off the source and can slice out of range.
func foldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
