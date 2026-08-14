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
	return fieldBody, "blank_transcript", "transcript text is blank; paste the transcript, or omit source_system: transcript to log the meeting without one"
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
	return fieldBody, "transcript_too_large", fmt.Sprintf(
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

// transcriptLines splits a NORMALIZED transcript body into its addressable
// lines. Index i holds line i+1, because the addressing this feature cites is
// 1-based (ADR-0058).
//
// It is one line of code and it is a function anyway, because it is the single
// spelling of the split. A reader that splits differently — trimming empties,
// or on a different separator — cites line numbers that disagree with what the
// human is looking at on screen, and the disagreement is invisible: both sides
// produce plausible numbers. Everything that addresses a transcript goes
// through here.
func transcriptLines(normalized string) []string {
	return strings.Split(normalized, "\n")
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

// A reading addresses at most this much text. Past it the read is refused
// rather than truncated: a proposal set covering the first half of a meeting is
// indistinguishable from one covering all of it.
//
// The bound lives here, beside what a transcript IS, because both the door and
// the engine have to agree on it — the door so a rep is told at once rather
// than after a queued job fails, the engine because the body can change between
// the two.
const (
	MaxReadableTranscriptLines = 600
	MaxReadableTranscriptChars = 60000
)

// TranscriptTooLongError maps to 422, naming the size of the thing the caller
// gave and the size a reading addresses, so the message says what to do.
type TranscriptTooLongError struct{ Lines, Chars int }

func (e *TranscriptTooLongError) Error() string {
	return fmt.Sprintf(
		"this transcript is %d lines / %d characters, and one reading addresses at most %d lines / %d characters; "+
			"log the meeting as more than one transcript and read each",
		e.Lines, e.Chars, MaxReadableTranscriptLines, MaxReadableTranscriptChars)
}

// FieldFault names the offending field; the caller's value is left to the
// wire's own field pointer, not interpolated into the message.
func (e *TranscriptTooLongError) FieldFault() (field, code, message string) {
	return "id", faultInvalid, e.Error()
}

// WithinReadingBounds refuses a transcript larger than one reading covers.
func WithinReadingBounds(lines []string) error {
	chars := 0
	for _, line := range lines {
		chars += len(line) + 1
	}
	if len(lines) > MaxReadableTranscriptLines || chars > MaxReadableTranscriptChars {
		return &TranscriptTooLongError{Lines: len(lines), Chars: chars}
	}
	return nil
}
