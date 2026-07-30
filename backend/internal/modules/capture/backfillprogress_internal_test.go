// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

// A page is a batch of independent messages and nothing promises a connector
// walks it serially, so two reports can arrive out of order. The tally takes
// the forward one and drops the rest: a count that goes backwards on screen
// reads as an import losing work it already did.
func TestPageTallyTakesOnlyForwardReports(t *testing.T) {
	cases := []struct {
		name                                   string
		reports                                [][3]int
		wantScanned, wantCaptured, wantSkipped int
	}{
		{
			name:        "an ordinary walk takes every report",
			reports:     [][3]int{{1, 1, 0}, {2, 1, 1}, {3, 2, 1}},
			wantScanned: 3, wantCaptured: 2, wantSkipped: 1,
		},
		{
			name:        "a report that arrives late and low is dropped whole",
			reports:     [][3]int{{3, 2, 1}, {2, 1, 1}},
			wantScanned: 3, wantCaptured: 2, wantSkipped: 1,
		},
		{
			name:        "a repeat of the held report changes nothing",
			reports:     [][3]int{{2, 2, 0}, {2, 0, 2}},
			wantScanned: 2, wantCaptured: 2, wantSkipped: 0,
		},
		{
			name:        "the walk resumes forward after a dropped report",
			reports:     [][3]int{{3, 3, 0}, {1, 1, 0}, {4, 4, 0}},
			wantScanned: 4, wantCaptured: 4, wantSkipped: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tally pageTally
			for _, r := range tc.reports {
				held := tally.scanned
				// The write is gated on this answer, so it has to mean exactly
				// "the report was ahead of what we held".
				if moved := tally.advance(r[0], r[1], r[2]); moved != (r[0] > held) {
					t.Fatalf("advance%v on a tally holding scanned=%d reported moved=%v", r, held, moved)
				}
			}
			if tally.scanned != tc.wantScanned || tally.captured != tc.wantCaptured || tally.skipped != tc.wantSkipped {
				t.Fatalf("tally = scanned %d / captured %d / skipped %d, want %d / %d / %d",
					tally.scanned, tally.captured, tally.skipped, tc.wantScanned, tc.wantCaptured, tc.wantSkipped)
			}
		})
	}
}
