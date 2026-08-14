// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import "testing"

// TestNormalizeTranscriptCanonicalizesLineEndingsAndWhitespace pins ADR-0058's
// normalization rule: split on newlines, trim trailing whitespace per line,
// 1-indexed. The stored form must be reproducible regardless of the pasted
// source's line-ending or trailing-space conventions, since a later reader
// cites a line by splitting the same way.
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
