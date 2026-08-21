// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Which words in a subject line could BE a project key. Pure text work, kept
// apart from the ladder that uses it because it is the one part of project
// attribution with no database in it — and because its rules are the whole
// reason the rung is safe to run without a human.

import "strings"

// projectKeyMinLen / projectKeyMaxLen bound a candidate to the shape the
// project_key_shape CHECK admits: letter-led, 2 to 24 characters of letters,
// digits, underscore and hyphen. A token outside that range can match no key
// that could ever have been created, so it never reaches the database.
const (
	projectKeyMinLen = 2
	projectKeyMaxLen = 24
)

// subjectPunctuation is what a subject wraps a key in — brackets, quotes and
// sentence punctuation. It is trimmed from each end of a word so "[ERP-27]:"
// offers "erp-27", and it deliberately excludes the underscore and the hyphen,
// which are legal INSIDE a key.
const subjectPunctuation = "[](){}<>\"'`.,:;!?/\\|*#+=~^%$&@"

// projectKeyCandidates answers which words in a subject could be a project key,
// lowercased for the case-insensitive match the key's uniqueness index is built
// on.
//
// A candidate is a WHOLE word, wrapping punctuation aside — never a fragment of
// one. That is the rung's safety: a substring match would file every message
// mentioning ERPNEXT under a project keyed ERP, and the ladder has no human in
// it to catch that. It is also why a word is rejected outright when any
// character in it is not key-legal, rather than being cut into pieces at that
// character: "grüße" is not evidence for a project keyed GR.
//
// Duplicates are dropped — the matcher asks the database one question about the
// whole set, and a subject repeating its key adds nothing to it.
func projectKeyCandidates(subject string) []string {
	var candidates []string
	seen := map[string]struct{}{}
	for _, word := range strings.Fields(subject) {
		token := strings.ToLower(strings.Trim(word, subjectPunctuation))
		if !projectKeyShaped(token) {
			continue
		}
		if _, repeat := seen[token]; repeat {
			continue
		}
		seen[token] = struct{}{}
		candidates = append(candidates, token)
	}
	return candidates
}

// projectKeyShaped reports whether a lowercased word could be a project key —
// the Go reading of the project_key_shape CHECK, applied to the WHOLE word.
//
// A bare number is excluded by the letter-led rule, which is the point of that
// rule: a subject line is full of dates, amounts and order numbers, and a key
// that could be one of those would attribute mail at random.
func projectKeyShaped(token string) bool {
	if len(token) < projectKeyMinLen || len(token) > projectKeyMaxLen {
		return false
	}
	if first := token[0]; first < 'a' || first > 'z' {
		return false
	}
	// Byte-wise, which is exact here: every byte this loop admits is a
	// single-byte character, so a multi-byte rune fails on its first byte and
	// the length bounds above count characters rather than bytes.
	for i := 1; i < len(token); i++ {
		if !projectKeyByte(token[i]) {
			return false
		}
	}
	return true
}

// projectKeyByte reports whether one lowercased byte is legal inside a key.
func projectKeyByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	default:
		return b == '_' || b == '-'
	}
}
