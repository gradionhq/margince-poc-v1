// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The summary is the one part of an approval that is prose, and several
// stagers build it out of record text an agent or an inbound sender wrote:
// a display name arrives from captured mail, and creating a person is
// auto-execute for an agent. So the text a human reads before deciding is
// attacker-influenced, and these cases pin what may reach their screen.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSummaryStripsWhatCanMisleadAReader(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a newline cannot start a second line": {
			"Archive \"Acme\"\nApproved by security", "Archive \"Acme\" Approved by security",
		},
		"a carriage return cannot overwrite the line": {
			"Archive \"Acme\"\r\r\r  Routine cleanup", "Archive \"Acme\" Routine cleanup",
		},
		"a bidi override cannot reverse what follows": {
			"Send to \u202emoc.rekcatta@ved\u202c", "Send to moc.rekcatta@ved",
		},
		"a directional isolate goes the same way": {
			"Merge \u2066victim\u2069 into \u2067attacker\u2069", "Merge victim into attacker",
		},
		"a zero-width joiner cannot build a homograph": {
			"Archive \"Ac\u200bme\"", "Archive \"Acme\"",
		},
		"ordinary text is left alone": {
			"Send offer to Jörg Müller (€2,500.00)", "Send offer to Jörg Müller (€2,500.00)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sanitizeSummary(tc.in); got != tc.want {
				t.Errorf("sanitizeSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A benign prefix long enough to push the real content out of view is the
// cheapest of these attacks and needs no special characters at all.
func TestSanitizeSummaryBoundsALongPrefix(t *testing.T) {
	got := sanitizeSummary(strings.Repeat("routine cleanup ", 200) + "and delete everything")
	if len(got) > maxSummaryLen {
		t.Errorf("summary is %d bytes, want at most %d — the ellipsis is inside the budget", len(got), maxSummaryLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated summary must say so, got %q", got[max(0, len(got)-40):])
	}
}

// Bounding must not produce invalid UTF-8. The cut has to land INSIDE a
// multi-byte rune for this to test anything: an ASCII run sized so that the
// budget falls partway through the 日 that follows it puts the naive cut
// between that rune's bytes, and only walking back to the rune start avoids
// emitting half of it.
func TestSanitizeSummaryCutsOnARuneBoundary(t *testing.T) {
	for offset := 1; offset <= 4; offset++ {
		prefix := strings.Repeat("x", maxSummaryLen-offset)
		got := sanitizeSummary(prefix + strings.Repeat("日", 8))
		if strings.ContainsRune(got, '�') {
			t.Errorf("bounding split a rune at offset=%d: %q", offset, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("bounding produced invalid UTF-8 at offset=%d: %q", offset, got)
		}
		if len(got) > maxSummaryLen {
			t.Errorf("bounded summary is %d bytes at offset=%d, want at most %d", len(got), offset, maxSummaryLen)
		}
	}
}
