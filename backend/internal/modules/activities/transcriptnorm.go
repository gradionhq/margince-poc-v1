// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// ADR-0058's line-addressing half of the canonical transcript form: split on
// newlines, trim trailing whitespace per line, 1-indexed. (The other half —
// parsing a leading `Speaker:` prefix into a structured field — is left
// inline in the line text rather than extracted, since nothing here persists
// per-line rows; a future reader that needs the speaker separately parses
// the same text the same way.) 1-indexing is a property of the STORED form
// rather than a separate persisted value — line N is the Nth newline-split
// segment (1-based) of activity.body — so any later reader reproduces the
// same index by splitting the identical way, with nothing to drift out of
// sync.

import (
	"fmt"
	"strings"
)

// maxTranscriptBytes bounds the raw paste, well under the ~950 KB a body can
// reach before Postgres's tsvector output (activity.search_tsv is a
// GENERATED column over subject+body) exceeds its own 1,048,575-byte limit
// and the write fails with an unmapped 54000 — a bound the request-size
// chassis cap (1 MiB) does not itself prevent.
const maxTranscriptBytes = 256 * 1024

// utf8BOM is a leading byte-order mark, stripped rather than treated as
// content — a file exported by a Windows tool and read to text client-side
// carries one, and a paste from the same source usually doesn't, so leaving
// it in would make the two land as different stored text for the same
// transcript.
const utf8BOM = "\uFEFF"

// blankTranscriptError maps to 422 (apperrors.FieldFault) — a whitespace-only
// paste is not a recording of anything, refused before it ever reaches a
// stored activity row.
type blankTranscriptError struct{}

func (e blankTranscriptError) Error() string {
	field, _, message := e.FieldFault()
	return field + ": " + message
}

func (blankTranscriptError) FieldFault() (field, code, message string) {
	return "body", "blank_transcript", "transcript text is blank; paste the transcript, or omit source_system: transcript to log the meeting without one"
}

// ErrBlankTranscript is the comparable sentinel a caller matches with
// errors.Is; it is also the concrete value returned, so the FieldFault
// httperr.Write needs is always reachable without an extra wrap.
var ErrBlankTranscript error = blankTranscriptError{}

// oversizedTranscriptError maps to 422 (apperrors.FieldFault).
type oversizedTranscriptError struct{ bytes int }

func (e oversizedTranscriptError) Error() string {
	field, _, message := e.FieldFault()
	return field + ": " + message
}

func (e oversizedTranscriptError) FieldFault() (field, code, message string) {
	return "body", "transcript_too_large", fmt.Sprintf(
		"transcript text is %d bytes, over the %d-byte limit; split it into more than one activity", e.bytes, maxTranscriptBytes)
}

// normalizeTranscript canonicalizes pasted or uploaded transcript text: a
// leading byte-order mark is stripped, CRLF and lone CR both fold to LF,
// NUL and every other C0 control byte but LF/TAB are stripped (Postgres
// refuses NUL outright, and the rest have no place in a spoken-word
// transcript), each line's trailing whitespace is trimmed, and the result
// is refused if it is blank or over the size bound.
func normalizeTranscript(raw string) (string, error) {
	if len(raw) > maxTranscriptBytes {
		return "", oversizedTranscriptError{bytes: len(raw)}
	}
	unified := strings.TrimPrefix(raw, utf8BOM)
	unified = strings.ReplaceAll(unified, "\r\n", "\n")
	unified = strings.ReplaceAll(unified, "\r", "\n")
	unified = stripOtherControlBytes(unified)
	// A trailing newline is line-ending punctuation, not a blank final turn —
	// dropping it here is what keeps a paste with an editor's final newline
	// and the identical paste without one producing the same stored form.
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

// stripOtherControlBytes drops every C0 control byte except the LF this
// function's caller splits lines on and the TAB a transcript's trailing-
// whitespace trim already handles — NUL above all, which Postgres refuses
// outright ("invalid byte sequence for encoding UTF8") rather than storing.
func stripOtherControlBytes(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
