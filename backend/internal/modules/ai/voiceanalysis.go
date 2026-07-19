// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The deterministic half of voice building. Measurements are computed from
// the user's own normalized text and given to the model as evidence; the model
// never invents the corpus statistics or the representative examples.

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	// VoiceBuilderVersion pins the deterministic analyzer/prompt contract.
	VoiceBuilderVersion = 1
	// StarterVoiceWords is the honest minimum for a provisional profile.
	StarterVoiceWords  = 800
	voicePromptWordCap = 12000
)

var sentenceBoundary = regexp.MustCompile(`[.!?]+(?:\s+|$)`)

// VoiceSample is one already consented, owner-only corpus source.
type VoiceSample struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Register  string  `json:"register"`
	Weight    float64 `json:"weight"`
	Text      string  `json:"text"`
	WordCount int     `json:"word_count"`
}

// VoiceStats is the deterministic style fingerprint used both by the model
// and by acceptance tests. Rates are per 100 words.
type VoiceStats struct {
	SampleCount           int            `json:"sample_count"`
	WordCount             int            `json:"word_count"`
	SentenceCount         int            `json:"sentence_count"`
	MeanSentenceWords     float64        `json:"mean_sentence_words"`
	MedianSentenceWords   float64        `json:"median_sentence_words"`
	SentenceWordStdDev    float64        `json:"sentence_word_stddev"`
	EmDashPer100Words     float64        `json:"em_dash_per_100_words"`
	QuestionPer100Words   float64        `json:"question_per_100_words"`
	ExclaimPer100Words    float64        `json:"exclaim_per_100_words"`
	EllipsisPer100Words   float64        `json:"ellipsis_per_100_words"`
	LineBreaksPer100Words float64        `json:"line_breaks_per_100_words"`
	RegisterWords         map[string]int `json:"register_words"`
	TopWords              []string       `json:"top_words"`
}

// AnalyzeVoice derives stable measurements without model judgment.
func AnalyzeVoice(samples []VoiceSample) VoiceStats {
	stats := VoiceStats{SampleCount: len(samples), RegisterWords: map[string]int{}}
	wordFreq := map[string]int{}
	var sentenceLengths []int
	var emDashes, questions, exclaims, ellipses, lineBreaks int
	for _, sample := range samples {
		words := strings.Fields(sample.Text)
		stats.WordCount += len(words)
		stats.RegisterWords[sample.Register] += len(words)
		for _, word := range words {
			normalized := normalizeStyleWord(word)
			if len([]rune(normalized)) >= 4 {
				wordFreq[normalized]++
			}
		}
		for _, sentence := range sentenceBoundary.Split(sample.Text, -1) {
			if count := len(strings.Fields(sentence)); count > 0 {
				sentenceLengths = append(sentenceLengths, count)
			}
		}
		emDashes += strings.Count(sample.Text, "—") + strings.Count(sample.Text, "–")
		questions += strings.Count(sample.Text, "?")
		exclaims += strings.Count(sample.Text, "!")
		ellipses += strings.Count(sample.Text, "…") + strings.Count(sample.Text, "...")
		lineBreaks += strings.Count(sample.Text, "\n")
	}
	stats.SentenceCount = len(sentenceLengths)
	stats.MeanSentenceWords, stats.MedianSentenceWords, stats.SentenceWordStdDev = distribution(sentenceLengths)
	stats.EmDashPer100Words = per100(emDashes, stats.WordCount)
	stats.QuestionPer100Words = per100(questions, stats.WordCount)
	stats.ExclaimPer100Words = per100(exclaims, stats.WordCount)
	stats.EllipsisPer100Words = per100(ellipses, stats.WordCount)
	stats.LineBreaksPer100Words = per100(lineBreaks, stats.WordCount)
	stats.TopWords = topStyleWords(wordFreq, 12)
	return stats
}

func normalizeStyleWord(word string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, word)
}

func distribution(values []int) (float64, float64, float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	var sum float64
	for _, value := range sorted {
		sum += float64(value)
	}
	mean := sum / float64(len(sorted))
	median := float64(sorted[len(sorted)/2])
	if len(sorted)%2 == 0 {
		median = float64(sorted[len(sorted)/2-1]+sorted[len(sorted)/2]) / 2
	}
	var variance float64
	for _, value := range sorted {
		delta := float64(value) - mean
		variance += delta * delta
	}
	return round2(mean), round2(median), round2(math.Sqrt(variance / float64(len(sorted))))
}

func per100(count, words int) float64 {
	if words == 0 {
		return 0
	}
	return round2(float64(count) * 100 / float64(words))
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }

type wordFrequency struct {
	word  string
	count int
}

func topStyleWords(freq map[string]int, limit int) []string {
	items := make([]wordFrequency, 0, len(freq))
	for word, count := range freq {
		items = append(items, wordFrequency{word: word, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].word < items[j].word
		}
		return items[i].count > items[j].count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.word)
	}
	return result
}

// SelectVoiceSamples bounds prompt size while preserving kind/register
// diversity. Each group gets a turn before weighting can add more samples.
func SelectVoiceSamples(samples []VoiceSample) []VoiceSample {
	ordered := append([]VoiceSample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i].Register + ":" + ordered[i].Kind
		right := ordered[j].Register + ":" + ordered[j].Kind
		if left == right {
			return ordered[i].Weight > ordered[j].Weight
		}
		return left < right
	})
	groups := map[string][]VoiceSample{}
	var keys []string
	for _, sample := range ordered {
		key := sample.Register + ":" + sample.Kind
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], sample)
	}
	var selected []VoiceSample
	words := 0
	for {
		progress := false
		for _, key := range keys {
			group := groups[key]
			if len(group) == 0 {
				continue
			}
			sample := group[0]
			groups[key] = group[1:]
			if words > 0 && words+sample.WordCount > voicePromptWordCap {
				continue
			}
			selected = append(selected, sample)
			words += sample.WordCount
			progress = true
		}
		if !progress || words >= voicePromptWordCap {
			break
		}
	}
	return selected
}
