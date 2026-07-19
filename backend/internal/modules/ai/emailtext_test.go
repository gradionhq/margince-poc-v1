// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

func TestExtractOwnEmailTextDropsHeadersQuotesAndDisclaimer(t *testing.T) {
	body := "From: me@example.com\nTo: you@example.com\n\nThanks for the discussion. I will send the revised plan tomorrow.\n\nBest,\nLars\n\nOn Tue, Alex wrote:\n> words from Alex"
	got := ExtractOwnEmailText(body)
	want := "Thanks for the discussion. I will send the revised plan tomorrow.\n\nBest,\nLars"
	if got != want {
		t.Fatalf("extracted = %q, want %q", got, want)
	}
}

func TestExcludeEmailFromVoiceDefaultsSensitiveAndTinyMailOut(t *testing.T) {
	if _, excluded := ExcludeEmailFromVoice("Private matter", "This is long enough to otherwise be included in a writing corpus for analysis and testing."); !excluded {
		t.Fatal("private marker was not excluded")
	}
	if _, excluded := ExcludeEmailFromVoice("Next step", "Thanks, yes."); !excluded {
		t.Fatal("tiny reply was not excluded")
	}
}
