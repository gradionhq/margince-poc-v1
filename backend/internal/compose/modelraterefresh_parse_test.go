// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
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
	shipped := rateExtractSystemFor(corpusFence(t, doc.System))
	if doc.System != shipped {
		t.Errorf("corpus system prompt differs from the shipped rate_extract prompt — the shipped prompt is uncertified.\n--- corpus ---\n%q\n--- shipped ---\n%q", doc.System, shipped)
	}
}

// TestFxExtractPromptMatchesCorpus is the FX twin of the above: the production
// fxExtractSystem const must be byte-identical to the aicert corpus scenario's
// system prompt, so the committed cert record certifies the exact prompt the FX
// producer sends.
func TestFxExtractPromptMatchesCorpus(t *testing.T) {
	raw, err := os.ReadFile("aicert/corpus/rate_extract/fx_grounded.yaml")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		System string `yaml:"system"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	shipped := fxExtractSystemFor(corpusFence(t, doc.System))
	if doc.System != shipped {
		t.Errorf("corpus system prompt differs from the shipped fx prompt — the shipped prompt is uncertified.\n--- corpus ---\n%q\n--- shipped ---\n%q", doc.System, shipped)
	}
}

// corpusFence recovers the example marker a corpus scenario was written with,
// so the shipped prompt can be rebuilt around the SAME boundary and compared
// byte for byte. The nonce is the one thing that legitimately differs between a
// scenario and a live call — every other character, the boundary sentence
// included, is what was certified and must still be what ships.
func corpusFence(t *testing.T, system string) promptfence.Fence {
	t.Helper()
	marker := regexp.MustCompile(`<(untrusted-[0-9a-f-]{36})>`).FindStringSubmatch(system)
	if marker == nil {
		t.Fatal("the corpus scenario names no fence marker: it cannot pin a prompt whose boundary is per-call")
	}
	var fence promptfence.Fence
	if err := json.Unmarshal([]byte(strconv.Quote(marker[1])), &fence); err != nil {
		t.Fatalf("corpus marker %q is not a fence marker: %v", marker[1], err)
	}
	return fence
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

func TestPricingSourcesFromMap(t *testing.T) {
	got := PricingSourcesFromMap(map[string]string{
		"gemini":    "https://g/p",
		"anthropic": "https://a/p",
		"blank":     "  ", // empty url skipped
	})
	// Sorted by provider, blank dropped.
	if len(got) != 2 {
		t.Fatalf("got %d sources, want 2 (blank-url dropped): %+v", len(got), got)
	}
	if got[0].Provider != "anthropic" || got[0].URL != "https://a/p" {
		t.Errorf("got[0] = %+v, want anthropic (sorted first)", got[0])
	}
	if got[1].Provider != "gemini" || got[1].URL != "https://g/p" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if PricingSourcesFromMap(nil) != nil {
		t.Error("nil map should yield nil")
	}
}
