// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

func TestDetectAIPatternsFindsStructuralTellsWithoutFlaggingRanges(t *testing.T) {
	text := "Here's the thing: it's not about tools, but transformation — are you ready?"
	violations := DetectAIPatterns(text)
	want := map[string]bool{"parenthetical_dash": true, "abstract_contrast": true, "canned_opener": true, "generic_cta": true}
	for _, violation := range violations {
		delete(want, violation.Code)
	}
	if len(want) != 0 {
		t.Fatalf("missing violations: %v; got %+v", want, violations)
	}
	if got := DetectAIPatterns("Revenue grew 2024–2026 because renewals improved."); len(got) != 0 {
		t.Fatalf("numeric range was flagged: %+v", got)
	}
}

func TestSanitizeAIPatternsPreservesNumericRanges(t *testing.T) {
	got := SanitizeAIPatterns("A short note — with context. 2024–2026 stays.")
	if got != "A short note, with context. 2024–2026 stays." {
		t.Fatalf("sanitize = %q", got)
	}
}
