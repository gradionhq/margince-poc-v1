// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// TestEmbedReindexRunningDetailTellsEachCallerWhatItCanChange pins the house
// rule that a refusal names what the caller can act on. The two callers of the
// 409 are in opposite positions: one has not forced and can, the other forced
// and was refused BY force's own predicate — telling that one to pass force
// names the single thing that cannot help it, and hides the fact that the run it
// tried to take over is alive and reporting.
func TestEmbedReindexRunningDetailTellsEachCallerWhatItCanChange(t *testing.T) {
	unforced := embedReindexRunningDetail(false, 3*time.Minute)
	if !strings.Contains(unforced, "pass force") {
		t.Fatalf("the unforced refusal = %q, want it to name force — the caller's way to take over a run that stopped moving", unforced)
	}

	forced := embedReindexRunningDetail(true, 3*time.Minute)
	if strings.Contains(forced, "pass force") {
		t.Fatalf("the forced refusal = %q, want it NOT to ask for the flag the caller already passed", forced)
	}
	if !strings.Contains(forced, "3m0s") {
		t.Fatalf("the forced refusal = %q, want it to name how long ago the run it tried to take over last reported", forced)
	}
	if !strings.Contains(forced, humanStaleWindow(reembedStaleAfter)) {
		t.Fatalf("the forced refusal = %q, want it to name the window that age was measured against", forced)
	}
}

// TestHumanProgressAgeReadsAsWordsUnderASecond covers the age a live run
// actually reports: a confirm racing a run that noted progress moments earlier
// would otherwise be told "0s ago", which reads as a broken clock rather than as
// a run that is plainly working.
func TestHumanProgressAgeReadsAsWordsUnderASecond(t *testing.T) {
	for _, d := range []time.Duration{-time.Second, 0, 400 * time.Millisecond} {
		if got := humanProgressAge(d); got != "less than a second" {
			t.Fatalf("humanProgressAge(%v) = %q, want %q", d, got, "less than a second")
		}
	}
	if got := humanProgressAge(90*time.Second + 400*time.Millisecond); got != "1m30s" {
		t.Fatalf("humanProgressAge(90.4s) = %q, want 1m30s (rounded to the marker's own resolution)", got)
	}
}

// TestTheStealWindowClearsTheReportingInterval is the one arithmetic relation
// between the two constants that has to hold: a run reporting on
// search.ReembedProgressStaleness must not be stealable for merely having
// reported on schedule. It is not a proof that a healthy run is safe — the legs
// between two reports are unbounded, which both constants say — but a window
// under the interval would dispossess runs that are demonstrably working.
func TestTheStealWindowClearsTheReportingInterval(t *testing.T) {
	if reembedStaleAfter <= search.ReembedProgressStaleness {
		t.Fatalf("reembedStaleAfter (%v) must exceed search.ReembedProgressStaleness (%v) — a run reporting exactly on the interval would be stealable",
			reembedStaleAfter, search.ReembedProgressStaleness)
	}
}
