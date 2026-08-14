// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"errors"
	"strings"
	"testing"
)

// TestNormalizeTranscriptCanonicalizesLineEndingsAndWhitespace pins the
// line-addressing half of ADR-0058's normalization rule: split on newlines,
// trim trailing whitespace per line, 1-indexed. The stored form must be
// reproducible regardless of the pasted or uploaded source's line-ending,
// byte-order mark, or trailing-space conventions, since a later reader cites
// a line by splitting the same way.
func TestNormalizeTranscriptCanonicalizesLineEndingsAndWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unlabelled single line",
			in:   "We agreed to follow up next week.",
			want: "We agreed to follow up next week.",
		},
		{
			name: "speaker-labelled multi-line, ADR-0058's example shape",
			in:   "Anna: Let's ship by Friday.\nBen: Works for me.",
			want: "Anna: Let's ship by Friday.\nBen: Works for me.",
		},
		{
			name: "windows line endings normalize to \\n",
			in:   "Anna: hello\r\nBen: hi\r\n",
			want: "Anna: hello\nBen: hi",
		},
		{
			name: "trailing whitespace trimmed per line",
			in:   "Anna: hello   \nBen: hi\t\n",
			want: "Anna: hello\nBen: hi",
		},
		{
			name: "blank lines between turns are preserved as empty lines",
			in:   "Anna: hello\n\nBen: hi",
			want: "Anna: hello\n\nBen: hi",
		},
		{
			name: "a leading byte-order mark is stripped",
			in:   "\uFEFFAnna: hello",
			want: "Anna: hello",
		},
		{
			name: "NUL and other C0 control bytes are stripped, a mid-line tab is kept",
			in:   "Anna:\x00 hel\x01lo\tthere",
			want: "Anna: hello\tthere",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTranscript(tc.in)
			if err != nil {
				t.Fatalf("normalizeTranscript(%q) returned an error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeTranscript(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeTranscriptRefusesBlankInput: a paste of only whitespace is not
// a transcript — refusing it here, before it ever reaches a stored activity
// row, is cheaper than a downstream reader discovering an empty recording.
func TestNormalizeTranscriptRefusesBlankInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n\t\n"} {
		if _, err := normalizeTranscript(in); err == nil {
			t.Errorf("normalizeTranscript(%q) = nil error, want a refusal", in)
		}
	}
}

// TestNormalizeTranscriptRefusesAnOversizedTranscript: activity.search_tsv is
// a GENERATED tsvector column over subject+body, and Postgres's tsvector
// output has its own hard 1,048,575-byte ceiling — well under the chassis's
// 1 MiB request cap. A transcript large enough to cross it must be refused
// here, as a typed 422, rather than surfacing as an unmapped 500 at the
// write.
func TestNormalizeTranscriptRefusesAnOversizedTranscript(t *testing.T) {
	oversized := strings.Repeat("a", maxTranscriptBytes+1)
	_, err := normalizeTranscript(oversized)
	var tooLarge oversizedTranscriptError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v, want oversizedTranscriptError", err)
	}
}
