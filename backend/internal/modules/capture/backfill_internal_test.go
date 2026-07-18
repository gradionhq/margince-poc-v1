// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

// backfillPageCursor is the resume seam: an absent or unreadable stored
// cursor restarts the walk from the window's first page (re-scanning is
// safe — the capture key dedupes), never crashes the run.
func TestBackfillPageCursorExtractsOrRestarts(t *testing.T) {
	cases := []struct {
		name   string
		cursor []byte
		want   string
	}{
		{"no cursor yet is the first page", nil, ""},
		{"empty cursor is the first page", []byte{}, ""},
		{"a malformed cursor restarts rather than crashing", []byte("{broken"), ""},
		{"a committed token resumes from it", []byte(`{"page_token":"tok-42"}`), "tok-42"},
		{"a cursor without the key is the first page", []byte(`{"other":"x"}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backfillPageCursor(tc.cursor); got != tc.want {
				t.Fatalf("backfillPageCursor(%q) = %q, want %q", tc.cursor, got, tc.want)
			}
		})
	}
}
