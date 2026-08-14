// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// ADR-0058's canonical transcript form: split on newlines, trim trailing
// whitespace per line, 1-indexed. 1-indexing is a property of the STORED
// form rather than a separate persisted value — line N is the Nth
// newline-split segment (1-based) of activity.body — so any later reader
// (PR B's next-step proposal citing a line) reproduces the same index by
// splitting the identical way, with nothing to drift out of sync.

import "strings"

// blankTranscriptError maps to 422 (apperrors.FieldFault) — a whitespace-only
// paste is not a recording of anything, refused before it ever reaches a
// stored activity row.
type blankTranscriptError struct{}

func (blankTranscriptError) Error() string { return "transcript text is blank" }

func (blankTranscriptError) FieldFault() (field, code, message string) {
	return "body", "blank_transcript", "transcript text is blank"
}

// ErrBlankTranscript is the comparable sentinel a caller matches with
// errors.Is; it is also the concrete value returned, so the FieldFault
// httperr.Write needs is always reachable without an extra wrap.
var ErrBlankTranscript error = blankTranscriptError{}

// normalizeTranscript canonicalizes pasted or uploaded transcript text: CRLF
// and lone CR both fold to LF, each line's trailing whitespace is trimmed,
// and the result is refused if nothing but whitespace survives.
func normalizeTranscript(raw string) (string, error) {
	unified := strings.ReplaceAll(raw, "\r\n", "\n")
	unified = strings.ReplaceAll(unified, "\r", "\n")
	// A trailing newline is line-ending punctuation, not a blank final turn —
	// dropping it here is what keeps "pasted with an editor's final newline"
	// and "pasted without one" produce the identical stored form.
	unified = strings.TrimSuffix(unified, "\n")
	lines := strings.Split(unified, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	normalized := strings.Join(lines, "\n")
	if strings.TrimSpace(normalized) == "" {
		return "", ErrBlankTranscript
	}
	return normalized, nil
}
