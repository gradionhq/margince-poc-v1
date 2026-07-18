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
	if !strings.Contains(out, `margince_ai_tokens_total{direction="out"} 5`) {
		t.Fatalf("tokens_total out missing/wrong:\n%s", out)
	}
}

// TestCallMetricsRendersEachFamilyExactlyOnce: observations from two
// sources landing on one collector must render each # HELP / # TYPE line
// and the unlabeled tokens_total series exactly once per WritePrometheus
// call — a duplicated family makes a strict Prometheus scraper reject
// the whole scrape.
func TestCallMetricsRendersEachFamilyExactlyOnce(t *testing.T) {
	m := newCallMetrics()
	m.observe(Call{Task: "cold_start", Tier: "cheap_cloud", Provider: "openai", TokensIn: 10, TokensOut: 5})
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
	if !strings.Contains(out, `margince_ai_tokens_total{direction="out"} 13`) {
		t.Fatalf("tokens_total out should sum both sources to 13:\n%s", out)
	}
}

// TestSeparatelyAssembledRoutersShareOneCollector pins the production
// wiring itself: every Router assembled in this process observes into the
// same process-wide collector, so /metrics reports one honest total across
// separately-constructed lanes and renders it once. Without this, two
// wired surfaces regressing to private collectors would silently split
// the totals while the collector-level tests above kept passing.
func TestSeparatelyAssembledRoutersShareOneCollector(t *testing.T) {
	a := assembleRouter(nil, nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, nil, nil, false, nil)
	b := assembleRouter(nil, nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, nil, nil, false, nil)
	if a.metrics != b.metrics {
		t.Fatal("two assembled routers hold different collectors; /metrics totals would split across lanes")
	}
	if a.metrics != sharedCallMetrics {
		t.Fatal("assembled router does not observe into the process-wide collector")
	}
}
