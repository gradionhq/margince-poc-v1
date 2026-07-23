// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRateExtractPromptMatchesCorpus turns the "certified = shipped" claim into
// a fitness function: the production rateExtractSystem const must be byte-
// identical to the aicert corpus scenario's system prompt, so the committed
// Gemini cert record certifies the exact prompt the producer sends. (Parsed
// directly rather than via aicert.LoadCorpus — aicert imports compose, so a
// compose-package test importing aicert would be an import cycle.)
func TestRateExtractPromptMatchesCorpus(t *testing.T) {
	raw, err := os.ReadFile("aicert/corpus/rate_extract/pricing_grounded.yaml")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		System string `yaml:"system"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if doc.System != rateExtractSystem {
		t.Errorf("corpus system prompt differs from rateExtractSystem — the shipped prompt is uncertified.\n--- corpus ---\n%q\n--- const ---\n%q", doc.System, rateExtractSystem)
	}
}

// A real sample captured from https://ai.google.dev/gemini-api/docs/pricing —
// the model-cost crawl's live target. It proves numberPassages turns a real
// (messy, free-tier-interleaved) pricing page into cited passages the
// rate_extract task grounds against.
func TestNumberPassagesOnRealGeminiSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gemini_pricing_reduced.txt")
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	numbered := numberPassages(string(raw))
	if !strings.HasPrefix(numbered, "[s0] ") {
		t.Fatalf("numbered text does not start with a passage id: %.40q", numbered)
	}
	if !strings.Contains(numbered, "$1.50") {
		t.Error("expected the captured input price $1.50 to survive numbering")
	}
	if strings.Contains(numbered, "\n\n") {
		t.Error("numberPassages left a blank line (empty lines must be dropped)")
	}
}

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
