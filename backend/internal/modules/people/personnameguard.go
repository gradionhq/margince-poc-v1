// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Screening the untrusted text a name is read from.
//
// A display name is a header field the SENDER chooses, so it is adversarial
// input before it is a name: it can be megabytes long, it can carry invisible
// controls that make the stored string render as somebody else, and it can
// spell a Latin name in a look-alike script. personname.go decides what a name
// SAYS; this file decides what is safe to read in the first place.

import (
	"strings"
	"unicode"
)

// maxNameInputRunes bounds what the parser will read. A display name is a
// header field an untrusted sender chooses, and the token cap is only consulted
// AFTER splitting — so an 8 MiB quoted string would be split, joined and handed
// to dedupe and the database before anything refused it. Real names are far
// shorter than this; anything longer is not a name being truncated, it is a
// payload being declined.
const maxNameInputRunes = 200

// tooLongToBeAName reports whether input exceeds the bound, counted in runes so
// a multi-byte script is not penalized for its encoding.
func tooLongToBeAName(s string) bool {
	if len(s) > maxNameInputRunes*4 { // cheap pre-check: max UTF-8 bytes per rune
		return true
	}
	return len([]rune(s)) > maxNameInputRunes
}

// stripBidiControls removes the Unicode directional overrides and isolates.
// They are zero-width, so a stored name keeps rendering as something else
// entirely while comparing unequal — the display-spoofing vector. Nothing
// legitimate in a person's name needs them: real right-to-left text lays itself
// out from its own characters.
func stripBidiControls(s string) string {
	if !strings.ContainsFunc(s, isBidiControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isBidiControl(r) {
			return -1
		}
		return r
	}, s)
}

// isBidiControl reports the explicit directional formatting characters:
// LRE/RLE/PDF/LRO/RLO (U+202A–U+202E) and LRI/RLI/FSI/PDI (U+2066–U+2069).
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// isMixedScriptSpoof reports a name mixing scripts that render alike — the
// homoglyph vector, e.g. Cyrillic U+0410 standing in for Latin "A" in "Аlice".
// Latin combined with a non-confusable script (Greek, Han, Hebrew, Arabic,
// Cyrillic on its own) is ordinary multilingual text; Latin INTERLEAVED with
// Cyrillic or Greek inside one name is the attack.
func isMixedScriptSpoof(name string) bool {
	var latin, confusable bool
	for _, r := range name {
		switch {
		case unicode.Is(unicode.Latin, r):
			latin = true
		case unicode.Is(unicode.Cyrillic, r) || unicode.Is(unicode.Greek, r):
			confusable = true
		}
	}
	return latin && confusable
}
