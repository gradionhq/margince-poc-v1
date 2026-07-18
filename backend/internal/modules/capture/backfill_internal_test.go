// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

// backfillPageCursor is the resume seam: an absent stored cursor is the
// window's first page, a committed token resumes from it, and a NON-empty
// but unreadable cursor is an error — never a silent restart, which would
// re-page the window and inflate the run's counters.
func TestBackfillPageCursorResumesOrRefuses(t *testing.T) {
	cases := []struct {
		name    string
		cursor  []byte
		want    string
		wantErr bool
	}{
		{name: "no cursor yet is the first page", cursor: nil, want: ""},
		{name: "empty cursor is the first page", cursor: []byte{}, want: ""},
		{name: "a malformed cursor is refused, not restarted", cursor: []byte("{broken"), wantErr: true},
		{name: "a committed token resumes from it", cursor: []byte(`{"page_token":"tok-42"}`), want: "tok-42"},
		{name: "a cursor without the key is the first page", cursor: []byte(`{"other":"x"}`), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := backfillPageCursor(tc.cursor)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("backfillPageCursor(%q) = %q, want the unreadable-cursor error", tc.cursor, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("backfillPageCursor(%q) = %q, %v — want %q", tc.cursor, got, err, tc.want)
			}
		})
	}
}
