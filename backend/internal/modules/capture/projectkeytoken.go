// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Which part of a subject line is a project key. Pure text work, kept apart
// from the ladder that uses it because it is the one part of project
// attribution with no database in it — and because its rule is the whole reason
// the rung is safe to run without a human.
//
// The rule is that a key only counts when it is BRACKETED: `[ERP-27] status`
// names ERP-27, and nothing else in that subject names anything. That is the
// convention migration 0131 describes for the column ("the short handle a human
// writes in a subject line"), and requiring the marker is what makes the rung
// safe. Reading bare words as keys instead would mean a project keyed RE
// swallowed every `Re:` reply in the installation, and one keyed STATUS or
// PROJECT would take most of the rest — silently, in bulk, onto records that
// later get stamped for years of retention.

import "strings"

// projectKeyMinLen / projectKeyMaxLen bound a candidate to the shape the
// project_key_shape CHECK admits: letter-led, 2 to 24 characters of letters,
// digits, underscore and hyphen. Anything outside that range can match no key
// that could ever have been created, so it never reaches the database.
const (
	projectKeyMinLen = 2
	projectKeyMaxLen = 24
)

// maxProjectKeyCandidates bounds how many bracketed groups one subject may
// offer. A subject is attacker-supplied text and each candidate is a bind in
// the matcher's query; a message carrying more brackets than this is not a
// human filing work under a project.
const maxProjectKeyCandidates = 8

// projectKeyCandidates answers which bracketed groups in a subject could be a
// project key, lowercased for the case-insensitive match the key's uniqueness
// index is built on.
//
// Only `[...]` counts. Parentheses and braces are deliberately not markers:
// `(re)` and `{urgent}` are ordinary prose, and admitting them would reopen the
// bare-word problem under different punctuation.
//
// The bracketed text must be the WHOLE key with nothing else inside it, so
// `[ERP-27]` offers erp-27 while `[ERP 27]`, `[FYI: ERP]` and `[]` offer
// nothing. A key is one token by construction — project_key_shape admits no
// space — so a bracket group containing a space was never a key reference.
//
// Duplicates are dropped: the matcher asks one question about the whole set,
// and a subject repeating its key adds nothing to it.
func projectKeyCandidates(subject string) []string {
	var candidates []string
	seen := map[string]struct{}{}
	for _, group := range bracketedGroups(subject) {
		token := strings.ToLower(strings.TrimSpace(group))
		if !projectKeyShaped(token) {
			continue
		}
		if _, repeat := seen[token]; repeat {
			continue
		}
		seen[token] = struct{}{}
		candidates = append(candidates, token)
		if len(candidates) == maxProjectKeyCandidates {
			return candidates
		}
	}
	return candidates
}

// bracketedGroups returns the text inside each `[...]` in order. An unclosed
// bracket yields nothing — a subject ending mid-marker names no key, and
// treating the rest of the line as one would turn a stray `[` into a match.
func bracketedGroups(subject string) []string {
	var groups []string
	rest := subject
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			return groups
		}
		rest = rest[open+1:]
		shut := strings.IndexByte(rest, ']')
		if shut < 0 {
			return groups
		}
		groups = append(groups, rest[:shut])
		rest = rest[shut+1:]
	}
}

// projectKeyShaped reports whether a lowercased token could be a project key —
// the Go reading of the project_key_shape CHECK, applied to the whole token.
//
// A bare number is excluded by the letter-led rule, which is the point of that
// rule: `[2026]` and `[4711]` are a year and an order number, and a key that
// could be one of those would attribute mail at random.
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
