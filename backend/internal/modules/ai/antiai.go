// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Deterministic anti-AI checks are the floor beneath every prompt. They are
// intentionally structural and bilingual; a model cannot opt out by ignoring
// an instruction buried in the voice profile.

import (
	"regexp"
	"strings"
)

// VoiceViolation is one deterministic tell a critic must remove.
type VoiceViolation struct {
	Code   string
	Detail string
}

var (
	abstractContrast = regexp.MustCompile(`(?i)\b(?:it(?:'s| is)|this is|es (?:ist|geht))\s+not\b[^.!?]{0,100}\b(?:but|sondern)\b|\bnot about\b[^.!?]{0,100}\b(?:but|sondern)\b`)
	cannedOpener     = regexp.MustCompile(`(?i)^\s*(?:here(?:'s| is) the thing|the truth is|let(?:'s| us) be honest|die wahrheit ist|mal ehrlich)\b`)
	genericCTA       = regexp.MustCompile(`(?i)\b(?:what do you think|agree\?|are you ready\?|is your (?:team|organization|unternehmen) ready|wie siehst du das\?|was denkst du\?)`)
	aiEse            = regexp.MustCompile(`(?i)\b(?:delve|unlock|leverage|game[- ]changer|transformative|ever[- ]evolving|navigate the complexities|synergy|paradigm shift|ganzheitlich|bahnbrechend|in einer sich ständig wandelnden welt)\b`)
)

// DetectAIPatterns reports calibrated structural AI tells in EN/DE copy.
func DetectAIPatterns(text string) []VoiceViolation {
	var violations []VoiceViolation
	if containsParentheticalDash(text) {
		violations = append(violations, VoiceViolation{Code: "parenthetical_dash", Detail: "replace parenthetical em/en dashes with normal sentence structure"})
	}
	if abstractContrast.MatchString(text) {
		violations = append(violations, VoiceViolation{Code: "abstract_contrast", Detail: "remove the abstract not-X-but-Y reframe and state the concrete point"})
	}
	if cannedOpener.MatchString(text) {
		violations = append(violations, VoiceViolation{Code: "canned_opener", Detail: "start with the actual context, not an influencer-style opener"})
	}
	if genericCTA.MatchString(text) {
		violations = append(violations, VoiceViolation{Code: "generic_cta", Detail: "replace the generic engagement question with a concrete next step"})
	}
	if aiEse.MatchString(text) {
		violations = append(violations, VoiceViolation{Code: "ai_ese", Detail: "replace corporate AI vocabulary with plain language"})
	}
	return violations
}

func containsParentheticalDash(text string) bool {
	runes := []rune(text)
	for i, r := range runes {
		if r != '—' && r != '–' {
			continue
		}
		previousDigit := i > 0 && runes[i-1] >= '0' && runes[i-1] <= '9'
		nextDigit := i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9'
		if previousDigit && nextDigit {
			continue
		}
		return true
	}
	return false
}

// SanitizeAIPatterns guarantees the hard punctuation rule after the rewrite
// pass. Semantic violations are reported, not guessed away mechanically.
func SanitizeAIPatterns(text string) string {
	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '—' || r == '–' {
			previousDigit := i > 0 && runes[i-1] >= '0' && runes[i-1] <= '9'
			nextDigit := i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9'
			if previousDigit && nextDigit {
				out = append(out, r)
				continue
			}
			for len(out) > 0 && out[len(out)-1] == ' ' {
				out = out[:len(out)-1]
			}
			out = append(out, ',', ' ')
			for i+1 < len(runes) && runes[i+1] == ' ' {
				i++
			}
			continue
		}
		out = append(out, r)
	}
	return strings.ReplaceAll(string(out), ",  ", ", ")
}
