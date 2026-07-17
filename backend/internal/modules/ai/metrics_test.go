// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

func TestCallMetricsWritePrometheus(t *testing.T) {
	m := newCallMetrics()
	m.observe(Call{Task: "cold_start", Tier: "cheap_cloud", Provider: "openai", TokensIn: 10, TokensOut: 5})
	m.observe(Call{Task: "cold_start", Tier: "cheap_cloud", Provider: "openai", ErrorSentinel: "provider_error"})
	var b strings.Builder
	m.WritePrometheus(&b)
	out := b.String()
	if !strings.Contains(out, `margince_ai_calls_total{provider="openai",task="cold_start",tier="cheap_cloud"} 2`) {
		t.Fatalf("calls_total missing/wrong:\n%s", out)
	}
	if !strings.Contains(out, `margince_ai_call_errors_total{provider="openai",task="cold_start",tier="cheap_cloud"} 1`) {
		t.Fatalf("errors_total missing/wrong:\n%s", out)
	}
	if !strings.Contains(out, `margince_ai_tokens_total{direction="in"} 10`) {
		t.Fatalf("tokens_total in missing/wrong:\n%s", out)
	}
}

// TestCallMetricsRendersEachFamilyExactlyOnce locks the invariant behind
// the shared-collector fix: two callers observing through what stand in
// for two separately-wired Router instances (coldStartOptions,
// offerDraftOptions) must still land on ONE collector, so a single
// WritePrometheus call emits each # HELP / # TYPE line and the unlabeled
// tokens_total series exactly once. A regression back to one callMetrics
// per Router would instead need two renders — which duplicates every
// family and makes a strict Prometheus scraper reject the whole scrape.
func TestCallMetricsRendersEachFamilyExactlyOnce(t *testing.T) {
	m := newCallMetrics()
	// "Source 1": coldStartOptions' Router.
	m.observe(Call{Task: "cold_start", Tier: "cheap_cloud", Provider: "openai", TokensIn: 10, TokensOut: 5})
	// "Source 2": offerDraftOptions' Router, same shared collector.
	m.observe(Call{Task: "offer_draft", Tier: "premium", Provider: "anthropic", TokensIn: 20, TokensOut: 8})

	var b strings.Builder
	m.WritePrometheus(&b)
	out := b.String()

	for _, name := range []string{
		"margince_ai_calls_total",
		"margince_ai_call_errors_total",
		"margince_ai_tokens_total",
	} {
		if n := strings.Count(out, "# HELP "+name+" "); n != 1 {
			t.Fatalf("# HELP %s appeared %d times, want 1:\n%s", name, n, out)
		}
		if n := strings.Count(out, "# TYPE "+name+" "); n != 1 {
			t.Fatalf("# TYPE %s appeared %d times, want 1:\n%s", name, n, out)
		}
	}
	if n := strings.Count(out, `margince_ai_tokens_total{direction="in"}`); n != 1 {
		t.Fatalf(`margince_ai_tokens_total{direction="in"} appeared %d times, want 1:%s%s`, n, "\n", out)
	}
	if n := strings.Count(out, `margince_ai_tokens_total{direction="out"}`); n != 1 {
		t.Fatalf(`margince_ai_tokens_total{direction="out"} appeared %d times, want 1:%s%s`, n, "\n", out)
	}
	if !strings.Contains(out, `margince_ai_tokens_total{direction="in"} 30`) {
		t.Fatalf("tokens_total in should sum both sources to 30:\n%s", out)
	}
}
