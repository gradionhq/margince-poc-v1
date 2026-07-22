// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func evalSamples(count int) []ai.VoiceSample {
	samples := make([]ai.VoiceSample, 0, count)
	registers := []string{"email", "spoken", "long_form"}
	for i := 0; i < count; i++ {
		text := strings.Repeat("useful sentence about the work. ", 100)
		samples = append(samples, ai.VoiceSample{
			ID: fmt.Sprintf("s-%d", i), Kind: "email", Register: registers[i%len(registers)],
			Text: text, WordCount: len(strings.Fields(text)),
		})
	}
	return samples
}

func TestSplitVoiceHeldOutIsDeterministicAndFloorSafe(t *testing.T) {
	samples := evalSamples(12)
	heldA, buildA := splitVoiceHeldOut(samples, "hash-x")
	heldB, buildB := splitVoiceHeldOut(samples, "hash-x")
	if len(heldA) != len(heldB) || len(buildA) != len(buildB) {
		t.Fatal("the split must be stable for one snapshot hash")
	}
	for i := range heldA {
		if heldA[i].ID != heldB[i].ID {
			t.Fatal("the split must pick identical held-out samples on rerun")
		}
	}
	if len(heldA) == 0 || len(heldA) > voiceEvalHeldOutPrompts {
		t.Fatalf("held-out = %d, want 1..%d", len(heldA), voiceEvalHeldOutPrompts)
	}
	held := map[string]bool{}
	buildWords := 0
	for _, sample := range heldA {
		held[sample.ID] = true
	}
	for _, sample := range buildA {
		if held[sample.ID] {
			t.Fatalf("sample %s is in both halves", sample.ID)
		}
		buildWords += sample.WordCount
	}
	if buildWords < ai.StarterVoiceWords {
		t.Fatalf("the split left %d build words, below the %d floor", buildWords, ai.StarterVoiceWords)
	}
	if heldOnly, build := splitVoiceHeldOut(samples[:1], "h"); len(heldOnly) != 0 || len(build) != 1 {
		t.Fatal("a single-sample corpus reserves nothing")
	}
}

func TestScoreVoiceCandidateActivationPolicy(t *testing.T) {
	artifact := ai.VoiceArtifact{Inference: ai.VoiceInference{
		IdentitySummary: "direct operator", Vocabulary: []string{"ship", "honest", "operational"},
		SignatureMoves: []ai.VoiceSignatureMove{{Move: "verdict first"}},
		Avoid:          []string{"filler"}, RegisterNotes: []string{"spoken blunter"},
	}}
	goodDrafts := func(score float64) []voiceEvalDraft {
		drafts := make([]voiceEvalDraft, 6)
		for i := range drafts {
			drafts[i] = voiceEvalDraft{body: "text", score: score}
		}
		return drafts
	}

	t.Run("routine pass auto-activates", func(t *testing.T) {
		result := scoreVoiceCandidate(artifact, goodDrafts(0.8), 0, true, nil)
		if result.Action != "auto_activated" || result.StatusCode != "" || result.Classification != "routine" {
			t.Fatalf("result = %+v", result)
		}
		if passed, ok := result.Evaluation["passed"].(bool); !ok || !passed {
			t.Fatal("evaluation must record passed=true")
		}
	})

	t.Run("material drift passes into review", func(t *testing.T) {
		predecessor := &ai.VoiceProfileVersion{
			ProfileJSON: map[string]any{"inference": map[string]any{
				"identity_summary": "completely different persona of florid prose",
				"vocabulary":       []any{"florid", "elaborate", "ornate", "baroque"},
				"signature_moves":  []any{map[string]any{"move": "long wandering openings"}},
			}},
			Evaluation: map[string]any{"candidate_median_voice_score": 0.7},
		}
		result := scoreVoiceCandidate(artifact, goodDrafts(0.8), 0, true, predecessor)
		if result.Action != "review_required" || result.Classification != "material" || result.StatusCode != "" {
			t.Fatalf("result = %+v", result)
		}
		if len(result.ReviewReasons) == 0 {
			t.Fatal("a material candidate must say why it needs review")
		}
	})

	t.Run("hard failures are a quality regression", func(t *testing.T) {
		result := scoreVoiceCandidate(artifact, goodDrafts(0.8), 3, true, nil)
		if result.Action != "review_required" || result.StatusCode != "quality_regression" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("scoring below the active version regresses", func(t *testing.T) {
		predecessor := &ai.VoiceProfileVersion{
			ProfileJSON: map[string]any{"inference": map[string]any{
				"identity_summary": "direct operator",
				"vocabulary":       []any{"ship", "honest", "operational"},
				"signature_moves":  []any{map[string]any{"move": "verdict first"}},
				"avoid":            []any{"filler"},
				"register_notes":   []any{"spoken blunter"},
			}},
			Evaluation: map[string]any{"candidate_median_voice_score": 0.9},
		}
		result := scoreVoiceCandidate(artifact, goodDrafts(0.7), 0, true, predecessor)
		if result.Action != "review_required" || result.StatusCode != "quality_regression" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("low median never activates", func(t *testing.T) {
		result := scoreVoiceCandidate(artifact, goodDrafts(0.3), 0, true, nil)
		if result.Action != "review_required" || result.StatusCode != "quality_regression" {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestVoiceGuidanceNamesTheNextBestAddition(t *testing.T) {
	spokenGap := voiceGuidance(ai.VoiceStats{WordCount: 9000, RegisterWords: map[string]int{"email": 9000}})
	if next, ok := spokenGap["next_best"].(string); !ok || !strings.Contains(next, "transcript") {
		t.Fatalf("spoken gap guidance = %v", spokenGap)
	}
	wordGap := voiceGuidance(ai.VoiceStats{WordCount: 25000, RegisterWords: map[string]int{"email": 12000, "spoken": 13000}})
	if next, ok := wordGap["next_best"].(string); !ok || !strings.Contains(next, "5000") {
		t.Fatalf("word gap guidance = %v", wordGap)
	}
	atTarget := voiceGuidance(ai.VoiceStats{WordCount: 31000, RegisterWords: map[string]int{"email": 15000, "spoken": 16000}})
	if gaps, ok := atTarget["register_gaps"].([]string); !ok || len(gaps) != 0 {
		t.Fatalf("at-target guidance = %v", atTarget)
	}
}

func TestStylometricProximityOrdersByCloseness(t *testing.T) {
	corpus := ai.AnalyzeVoice(evalSamples(4))
	near := stylometricProximity(corpus, "Useful sentence about the work. Another useful sentence about the work.")
	far := stylometricProximity(corpus, "Why?! Really?! No way! Stop! Now! Go! Yes! What?! How?! Never!")
	if near <= far {
		t.Fatalf("proximity near=%f far=%f; a corpus-like draft must score closer", near, far)
	}
	if near < 0 || near > 1 || far < 0 || far > 1 {
		t.Fatalf("proximity out of [0,1]: %f / %f", near, far)
	}
}

// scriptedEvalBrain serves the evaluation's draft and judge calls.
type scriptedEvalBrain struct {
	judgeScore float64
	draftCalls int
	judgeCalls int
	budgetAt   int // fail with budget exhaustion on the Nth draft call (0 = never)
}

func (s *scriptedEvalBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	if strings.Contains(req.System, "compare drafts") {
		s.judgeCalls++
		count := strings.Count(req.Messages[0].Content, "<draft ")
		scores := make([]float64, count)
		for i := range scores {
			scores[i] = s.judgeScore
		}
		payload, err := json.Marshal(map[string]any{"scores": scores})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload)}, nil
	}
	s.draftCalls++
	if s.budgetAt > 0 && s.draftCalls >= s.budgetAt {
		return model.Response{}, ai.ErrBudgetDeferred
	}
	payload, err := json.Marshal(map[string]string{
		"subject": "Re: the work",
		"body":    "Useful sentence about the work. We ship Monday and the plan holds.",
	})
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: string(payload)}, nil
}

func TestEvaluateVoiceCandidateScoresHeldOutDrafts(t *testing.T) {
	samples := evalSamples(8)
	heldOut, buildSamples := splitVoiceHeldOut(samples, "hash-e")
	artifact := ai.VoiceArtifact{
		Markdown: "# Voice DNA\n\n## Identity\n\ndirect", Stats: ai.AnalyzeVoice(buildSamples),
		Inference: ai.VoiceInference{IdentitySummary: "direct"},
	}
	brain := &scriptedEvalBrain{judgeScore: 0.9}
	result, err := evaluateVoiceCandidate(context.Background(), brain, artifact, "", heldOut, nil)
	if err != nil {
		t.Fatal(err)
	}
	if brain.draftCalls != len(heldOut)*voiceEvalRepeatsPerPrompt {
		t.Fatalf("draft calls = %d, want %d", brain.draftCalls, len(heldOut)*voiceEvalRepeatsPerPrompt)
	}
	if brain.judgeCalls != len(heldOut) {
		t.Fatalf("judge calls = %d, want one per held-out prompt (%d)", brain.judgeCalls, len(heldOut))
	}
	if result.Action != "auto_activated" {
		t.Fatalf("a clean high-scoring first build must auto-activate, got %+v", result)
	}
	if len(result.SampleDrafts) != 2 {
		t.Fatalf("sample drafts = %d, want the 2 best kept", len(result.SampleDrafts))
	}
	median, ok := result.Evaluation["candidate_median_voice_score"].(float64)
	if !ok || median <= 0 || median > 1 {
		t.Fatalf("median = %v", result.Evaluation["candidate_median_voice_score"])
	}
}

func TestEvaluateVoiceCandidatePropagatesBudgetExhaustion(t *testing.T) {
	samples := evalSamples(8)
	heldOut, buildSamples := splitVoiceHeldOut(samples, "hash-b")
	artifact := ai.VoiceArtifact{Markdown: "# Voice DNA", Stats: ai.AnalyzeVoice(buildSamples)}
	_, err := evaluateVoiceCandidate(context.Background(), &scriptedEvalBrain{judgeScore: 0.9, budgetAt: 2},
		artifact, "", heldOut, nil)
	if !errors.Is(err, ai.ErrBudgetDeferred) {
		t.Fatalf("err = %v, want the budget sentinel to survive wrapping", err)
	}
}
