// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"os"
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
	marker, declared := promptfence.MarkerIn(system)
	if !declared {
		t.Fatal("the corpus scenario names no fence marker: it cannot pin a prompt whose boundary is per-call")
	}
	fence, ok := promptfence.FromMarker(marker)
	if !ok {
		t.Fatalf("corpus marker %q is not one this package could have minted", marker)
	}
	return fence
}

// The request is the whole security perimeter of this site: a pricing page is
// published by someone this system has never met, and its bytes reach the model
// numbered but unedited. The only thing that stops them ending their span is a
// marker minted for THIS call and named in THIS call's system prompt, so a
// request that fenced under some other marker — or repeated the page beside the
// span — would hand the instruction region to whoever publishes the page.
func TestRateExtractRequestFencesThePageUnderTheMarkerItDeclares(t *testing.T) {
	page := "Aurora Large. Input $5.00 / 1M tokens.\n\nAurora Mini. Input $0.25 / 1M tokens."

	req := rateExtractRequest(page)

	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the rate-extract system prompt declares no data boundary: %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want the single user turn", len(req.Messages))
	}
	content := req.Messages[0].Content
	openTag, closeTag := "<"+marker+">", "</"+marker+">"
	openAt, closeAt := strings.Index(content, openTag), strings.Index(content, closeTag)
	if openAt < 0 || closeAt < openAt {
		t.Fatalf("the page is not wrapped in the declared marker:\n%s", content)
	}
	span := content[openAt+len(openTag) : closeAt]
	for _, line := range strings.Split(page, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(span, line) {
			t.Errorf("page line %q never reached the fenced span:\n%s", line, content)
		}
		// Containment is a question of counts, not membership: a prompt that
		// keeps the fence and ALSO repeats the page beside it puts that copy in
		// the instruction region while "is it inside?" stays true.
		if n := strings.Count(content, line); n != 1 {
			t.Errorf("page line %q appears %d times, want only the fenced one:\n%s", line, n, content)
		}
	}
	if !strings.Contains(span, "[s0] ") || !strings.Contains(span, "[s1] ") {
		t.Errorf("the fenced page carries no cited passages, so no row could be grounded:\n%s", span)
	}
}

// A fence's scope is one call. A marker a page's author was shown is a marker
// they can spell, so reusing one would give away the only thing they cannot
// forge.
func TestRateExtractRequestMintsAFreshBoundaryPerCall(t *testing.T) {
	first, declared := promptfence.MarkerIn(rateExtractRequest("Input $5.00 / 1M tokens.").System)
	if !declared {
		t.Fatal("the rate-extract system prompt declares no data boundary")
	}
	second, declared := promptfence.MarkerIn(rateExtractRequest("Input $5.00 / 1M tokens.").System)
	if !declared {
		t.Fatal("the second rate-extract system prompt declares no data boundary")
	}
	if first == second {
		t.Errorf("two rate-extract requests share the boundary %q", first)
	}
}

// The gate is what keeps an ungrounded price out of an administrator's approval
// queue, and each refusal below is a distinct way a reply can fail to earn one.
// The provider is the case the sheet's identity depends on: a page must never
// stage a rate under a provider it does not own.
func TestAcceptRateRowsDropsEveryUngroundedRow(t *testing.T) {
	row := func(id, evidence, confidence string) extractedModel {
		return extractedModel{
			Provider: "aurora", ModelID: id, InputUsd: "5", OutputUsd: "25",
			CacheReadUsd: "0", CacheWriteUsd: "0", Evidence: evidence, Confidence: confidence,
		}
	}
	cases := []struct {
		name string
		in   extractedModel
		want bool
	}{
		{name: "a grounded row is kept", in: row("large", "s0", "0.95"), want: true},
		{name: "the floor itself is kept", in: row("large", "s0", "0.5"), want: true},
		{name: "a padded id is trimmed and kept", in: row("  large  ", "s0", "0.95"), want: true},
		{name: "no model named", in: row("   ", "s0", "0.95")},
		{name: "no passage cited", in: row("large", "  ", "0.95")},
		{name: "confidence below the floor", in: row("large", "s0", "0.49")},
		{name: "confidence above one", in: row("large", "s0", "1.5")},
		{name: "confidence that is not a number", in: row("large", "s0", "high")},
		{name: "confidence that is not finite", in: row("large", "s0", "NaN")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept := acceptRateRows([]extractedModel{tc.in}, "acme")
			if (len(kept) == 1) != tc.want {
				t.Fatalf("kept %d rows, want kept=%v", len(kept), tc.want)
			}
			if !tc.want {
				return
			}
			if kept[0].Provider != "acme" {
				t.Errorf("the kept row is filed under %q, want the configured source acme", kept[0].Provider)
			}
			if kept[0].ModelID != "large" {
				t.Errorf("the kept row names %q, want the trimmed id the write path stores", kept[0].ModelID)
			}
		})
	}
}

// The gate reads its input rather than consuming it: a caller that wants to say
// WHICH rows were refused still has the reply the model sent.
func TestAcceptRateRowsLeavesTheClaimedRowsReadable(t *testing.T) {
	claimed := []extractedModel{
		{ModelID: "ungrounded", Evidence: "", Confidence: "0.9"},
		{ModelID: "large", Evidence: "s0", Confidence: "0.9"},
	}

	kept := acceptRateRows(claimed, "acme")

	if len(kept) != 1 || kept[0].ModelID != "large" {
		t.Fatalf("kept %+v, want the one grounded row", kept)
	}
	if claimed[0].ModelID != "ungrounded" || claimed[1].ModelID != "large" {
		t.Errorf("the gate rewrote the rows it was given: %+v", claimed)
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
