// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestParseModelPricingSources(t *testing.T) {
	got := ParseModelPricingSources(" anthropic=https://a/p, openai=https://o/p , malformed , =https://x , gemini= ")
	if len(got) != 2 {
		t.Fatalf("parsed %d sources, want 2 (malformed/empty-provider/empty-url skipped): %+v", len(got), got)
	}
	if got[0].Provider != "anthropic" || got[0].URL != "https://a/p" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Provider != "openai" || got[1].URL != "https://o/p" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if ParseModelPricingSources("") != nil {
		t.Error("empty spec should yield nil")
	}
}
