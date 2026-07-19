// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestParseJudgeVerdictAcceptsTheStrictShape(t *testing.T) {
	v, err := parseJudgeVerdict(`{"score": 82, "reason": "grounded, on-topic"}`)
	if err != nil {
		t.Fatalf("valid judge output rejected: %v", err)
	}
	if v.Score != 82 || v.Reason != "grounded, on-topic" {
		t.Fatalf("parsed %+v, want score=82 reason=%q", v, "grounded, on-topic")
	}
}

func TestParseJudgeVerdictRefusesInvalidJSON(t *testing.T) {
	if _, err := parseJudgeVerdict("not json at all"); err == nil {
		t.Fatal("want an error for non-JSON judge output")
	}
}

func TestParseJudgeVerdictRefusesAnOutOfRangeScore(t *testing.T) {
	cases := []string{
		`{"score": 101, "reason": "too high"}`,
		`{"score": -1, "reason": "negative"}`,
	}
	for _, raw := range cases {
		if _, err := parseJudgeVerdict(raw); err == nil {
			t.Fatalf("want an error for out-of-range score in %q", raw)
		}
	}
}

func TestSelfJudgedComparesTheResolvedIdentities(t *testing.T) {
	cases := []struct {
		name             string
		candidate, judge string
		want             bool
	}{
		{"identical", "anthropic:claude-sonnet-4-6", "anthropic:claude-sonnet-4-6", true},
		{"different", "anthropic:claude-haiku", "anthropic:claude-opus", false},
		{"empty candidate never counts as a match", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selfJudged(c.candidate, c.judge); got != c.want {
				t.Fatalf("selfJudged(%q, %q) = %v, want %v", c.candidate, c.judge, got, c.want)
			}
		})
	}
}

func TestCloudServedNamesOnlyNetworkHostedVendors(t *testing.T) {
	local := []string{"ollama", "vllm", ai.ProviderFake}
	for _, p := range local {
		if cloudServed(p) {
			t.Errorf("cloudServed(%q) = true, want false (local inference)", p)
		}
	}
	cloud := []string{"anthropic", "openai", "gemini", "openai_compatible"}
	for _, p := range cloud {
		if !cloudServed(p) {
			t.Errorf("cloudServed(%q) = false, want true (network-hosted)", p)
		}
	}
}

func TestCheckCapsGatesTokensAndCloudOnlyLatency(t *testing.T) {
	t.Run("within every cap", func(t *testing.T) {
		ok, failures := checkCaps(Caps{MaxTokens: 100, P95LatencyMS: 5000}, ai.Call{TokensIn: 30, TokensOut: 30, LatencyMS: 1000, Provider: "anthropic"})
		if !ok || len(failures) != 0 {
			t.Fatalf("want ok with no failures, got ok=%v failures=%v", ok, failures)
		}
	})
	t.Run("max_tokens exceeded", func(t *testing.T) {
		ok, failures := checkCaps(Caps{MaxTokens: 50}, ai.Call{TokensIn: 40, TokensOut: 40, Provider: "anthropic"})
		if ok || len(failures) != 1 {
			t.Fatalf("want one max_tokens failure, got ok=%v failures=%v", ok, failures)
		}
	})
	t.Run("p95 latency exceeded on a cloud provider", func(t *testing.T) {
		ok, failures := checkCaps(Caps{P95LatencyMS: 500}, ai.Call{LatencyMS: 900, Provider: "anthropic"})
		if ok || len(failures) != 1 {
			t.Fatalf("want one p95 latency failure, got ok=%v failures=%v", ok, failures)
		}
	})
	t.Run("p95 latency ignored on a local provider", func(t *testing.T) {
		ok, failures := checkCaps(Caps{P95LatencyMS: 500}, ai.Call{LatencyMS: 900, Provider: ai.ProviderFake})
		if !ok || len(failures) != 0 {
			t.Fatalf("a local provider's latency must never fail the cap, got ok=%v failures=%v", ok, failures)
		}
	})
}
