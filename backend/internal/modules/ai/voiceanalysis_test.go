// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

func TestAnalyzeVoiceMeasuresTheCorpusDeterministically(t *testing.T) {
	samples := []VoiceSample{
		{ID: "a", Kind: "email", Register: "email", Text: "Send the offer today. Can you confirm the price?", WordCount: 9},
		{ID: "b", Kind: "transcript", Register: "spoken", Text: "We shipped it! That was the whole point…", WordCount: 8},
	}
	stats := AnalyzeVoice(samples)
	if stats.SampleCount != 2 || stats.WordCount != 17 {
		t.Fatalf("counts = %d samples / %d words, want 2 / 17", stats.SampleCount, stats.WordCount)
	}
	if stats.RegisterWords["email"] != 9 || stats.RegisterWords["spoken"] != 8 {
		t.Fatalf("register split = %v", stats.RegisterWords)
	}
	if stats.QuestionPer100Words == 0 || stats.ExclaimPer100Words == 0 || stats.EllipsisPer100Words == 0 {
		t.Fatalf("punctuation rates missing: %+v", stats)
	}
	if stats.SentenceCount != 4 {
		t.Fatalf("sentence count = %d, want 4", stats.SentenceCount)
	}
	again := AnalyzeVoice(samples)
	if again.MeanSentenceWords != stats.MeanSentenceWords || len(again.TopWords) != len(stats.TopWords) {
		t.Fatal("AnalyzeVoice is not deterministic over identical input")
	}
}

func TestSelectVoiceSamplesBoundsThePromptAndKeepsDiversity(t *testing.T) {
	var samples []VoiceSample
	long := strings.Repeat("word ", 5000)
	samples = append(samples,
		VoiceSample{ID: "email-1", Kind: "email", Register: "email", Text: long, WordCount: 5000},
		VoiceSample{ID: "email-2", Kind: "email", Register: "email", Text: long, WordCount: 5000},
		VoiceSample{ID: "email-3", Kind: "email", Register: "email", Text: long, WordCount: 5000},
		VoiceSample{ID: "spoken-1", Kind: "transcript", Register: "spoken", Text: long, WordCount: 5000},
	)
	selected := SelectVoiceSamples(samples)
	words := 0
	sawSpoken := false
	for _, sample := range selected {
		words += sample.WordCount
		if sample.Register == "spoken" {
			sawSpoken = true
		}
	}
	// The cap admits the sample that crosses it, never a later one: the
	// selection stays within one sample of the cap and keeps every register.
	if words > voicePromptWordCap+5000 {
		t.Fatalf("selected %d words, cap is %d", words, voicePromptWordCap)
	}
	if !sawSpoken {
		t.Fatal("register diversity lost: no spoken sample selected")
	}
}

func TestSelectExemplarsKeepsExactlyTwoVerbatimDistinctRegisters(t *testing.T) {
	samples := []VoiceSample{
		{ID: "a", Kind: "email", Register: "email", Text: "Short and direct. We ship on Monday.", WordCount: 7},
		{ID: "b", Kind: "transcript", Register: "spoken", Text: "Look, the point is simple. It works.", WordCount: 8},
		{ID: "c", Kind: "email", Register: "email", Text: "Another email in the same register entirely.", WordCount: 7},
	}
	stats := AnalyzeVoice(samples)
	exemplars := SelectExemplars(samples, stats)
	if len(exemplars) != 2 {
		t.Fatalf("exemplars = %d, want exactly 2", len(exemplars))
	}
	if exemplars[0].Register == exemplars[1].Register {
		t.Fatalf("both exemplars share register %q; distinct registers preferred", exemplars[0].Register)
	}
	for _, exemplar := range exemplars {
		found := false
		for _, sample := range samples {
			if strings.Contains(strings.Join(strings.Fields(sample.Text), " "), exemplar.Text) {
				found = true
			}
		}
		if !found {
			t.Fatalf("exemplar %q is not verbatim corpus text", exemplar.Text)
		}
	}
}

func TestSelectExemplarsTruncatesLongSamples(t *testing.T) {
	long := strings.Repeat("verbose ", 400)
	samples := []VoiceSample{{ID: "a", Kind: "document", Register: "long_form", Text: long, WordCount: 400}}
	exemplars := SelectExemplars(samples, AnalyzeVoice(samples))
	if len(exemplars) != 1 {
		t.Fatalf("exemplars = %d, want 1 from a one-sample corpus", len(exemplars))
	}
	if got := len(strings.Fields(exemplars[0].Text)); got > exemplarWordCap+1 {
		t.Fatalf("exemplar is %d words, cap is %d", got, exemplarWordCap)
	}
	if !strings.HasSuffix(exemplars[0].Text, "…") {
		t.Fatal("a truncated exemplar must say so")
	}
}
