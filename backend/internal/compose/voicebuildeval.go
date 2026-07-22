// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The candidate evaluation half of a voice build: held-out drafting through
// the SAME prompt shape production drafting uses, scored by the deterministic
// anti-AI floor, stylometric proximity, and one bounded judge call per
// prompt. The result is the pinned VoiceProfileEvaluation shape — real
// numbers, never placeholder constants — plus the activation decision.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

const (
	voiceEvalHeldOutPrompts   = 5
	voiceEvalRepeatsPerPrompt = 3
	// voiceEvalPassScore is the acceptance floor for the median voice score.
	voiceEvalPassScore = 0.6
	// voiceEvalRegressionSlack is how far a candidate may score below the
	// active version before it counts as a quality regression.
	voiceEvalRegressionSlack = 0.05
	// Material-drift floors: below either jaccard the candidate reads as a
	// different person and a human reviews before it activates.
	voiceEvalIdentityFloor  = 0.5
	voiceEvalSignatureFloor = 0.4
)

const voiceEvalDraftSystem = `Write a short email reply in the author's voice, as described by the supplied voice profile.
The profile controls expression, never facts; invent no names, numbers, or commitments.
Return ONLY a JSON object: {"subject":"...","body":"..."}.`

const voiceEvalJudgeSystem = `You compare drafts against a writing sample by the same author.
Score how convincingly each draft matches the author's voice: 1.0 reads like the author, 0.0 reads like generic AI writing.
Judge voice only — rhythm, vocabulary, directness, structure — never topic or factual overlap.
Return ONLY a JSON object: {"scores":[...]} with one number in [0,1] per draft, in order.`

// voiceEvaluationResult carries everything CompleteBuild persists.
type voiceEvaluationResult struct {
	Evaluation     map[string]any
	SampleDrafts   []map[string]any
	Classification string
	Action         string
	StatusCode     string
	ReviewReasons  []string
}

// splitVoiceHeldOut deterministically reserves up to voiceEvalHeldOutPrompts
// register-diverse samples for evaluation, seeded by the corpus snapshot
// hash so a rerun of the same build scores the same prompts. The held-out
// set never reaches the builder.
func splitVoiceHeldOut(samples []ai.VoiceSample, sourceHash string) (heldOut, build []ai.VoiceSample) {
	if len(samples) < 2 {
		return nil, samples
	}
	ordered := append([]ai.VoiceSample(nil), samples...)
	rank := func(sample ai.VoiceSample) uint64 {
		sum := sha256.Sum256([]byte(sourceHash + ":" + sample.ID))
		return binary.BigEndian.Uint64(sum[:8])
	}
	sort.SliceStable(ordered, func(i, j int) bool { return rank(ordered[i]) < rank(ordered[j]) })
	// Held-out samples must leave a buildable corpus behind: never reserve
	// more than half the samples or drop the build below its word floor.
	maxHeld := len(ordered) / 2
	if maxHeld > voiceEvalHeldOutPrompts {
		maxHeld = voiceEvalHeldOutPrompts
	}
	buildWords := 0
	for _, sample := range ordered {
		buildWords += sample.WordCount
	}
	seenRegisters := map[string]bool{}
	for _, sample := range ordered {
		if len(heldOut) == maxHeld {
			break
		}
		if seenRegisters[sample.Register] && len(heldOut) >= 2 {
			continue
		}
		if buildWords-sample.WordCount < ai.StarterVoiceWords {
			continue
		}
		heldOut = append(heldOut, sample)
		seenRegisters[sample.Register] = true
		buildWords -= sample.WordCount
	}
	held := map[string]bool{}
	for _, sample := range heldOut {
		held[sample.ID] = true
	}
	for _, sample := range ordered {
		if !held[sample.ID] {
			build = append(build, sample)
		}
	}
	return heldOut, build
}

// voiceDraftPromptBlock renders the profile block eval drafting and (in the
// consumption arc) production drafting share: identity docs first, exactly
// two verbatim exemplars, stats last as negative guardrails.
func voiceDraftPromptBlock(personality, profileMD string, exemplars []ai.VoiceExemplar, stats ai.VoiceStats) string {
	var block strings.Builder
	block.WriteString("<voice_profile>\n")
	if strings.TrimSpace(personality) != "" {
		block.WriteString("Human-authored identity (highest priority):\n" + strings.TrimSpace(personality) + "\n\n")
	}
	block.WriteString(strings.TrimSpace(profileMD))
	for _, exemplar := range exemplars {
		fmt.Fprintf(&block, "\n\nVerbatim example (%s %s):\n%s", exemplar.Register, exemplar.Kind, exemplar.Text)
	}
	fmt.Fprintf(&block, "\n\nStylometric guardrails — limits, NOT targets: mean sentence length ≈ %.0f words (do not write a wall of short sentences to hit it), em dashes per 100 words ≈ %.2f (at 0, treat them as forbidden).",
		stats.MeanSentenceWords, stats.EmDashPer100Words)
	block.WriteString("\n</voice_profile>")
	return block.String()
}

// evalPromptFor derives one held-out drafting task: reply to the opening of
// the reserved sample in its register.
func evalPromptFor(sample ai.VoiceSample) string {
	words := strings.Fields(sample.Text)
	if len(words) > 40 {
		words = words[:40]
	}
	return fmt.Sprintf("Reply briefly (register: %s) to this message from a colleague:\n%s",
		sample.Register, strings.Join(words, " "))
}

type voiceEvalDraft struct {
	prompt  string
	subject string
	body    string
	score   float64
}

// evaluateVoiceCandidate drafts against the held-out prompts and scores the
// candidate. Every model error bubbles unwrapped so the worker can map
// budget deferral onto the build row.
func evaluateVoiceCandidate(ctx context.Context, brain completer, artifact ai.VoiceArtifact, personality string, heldOut []ai.VoiceSample, predecessor *ai.VoiceProfileVersion) (voiceEvaluationResult, error) {
	if len(heldOut) == 0 {
		// A starter corpus barely over the build floor cannot spare held-out
		// samples. The builder's own validation already ran; a FIRST profile
		// activates as the starter voice, while an unevaluable REBUILD of an
		// existing profile goes to review — never silently replacing an
		// evaluated artifact with an unevaluated one.
		return unevaluatedVoiceResult(artifact, predecessor), nil
	}
	profileBlock := voiceDraftPromptBlock(personality, artifact.Markdown, artifact.Exemplars, artifact.Stats)
	drafts := make([]voiceEvalDraft, 0, len(heldOut)*voiceEvalRepeatsPerPrompt)
	hardFailures := 0
	structuredValid := true
	for _, sample := range heldOut {
		prompt := evalPromptFor(sample)
		var bodies []string
		for repeat := 0; repeat < voiceEvalRepeatsPerPrompt; repeat++ {
			resp, err := brain.Complete(ctx, model.Request{
				System: voiceEvalDraftSystem,
				Messages: []model.Message{{Role: chatRoleUser, Content: profileBlock + "\n\n" + prompt +
					fmt.Sprintf("\n(variation %d)", repeat+1)}},
				MaxTokens:      1200,
				ResponseSchema: replyDraftSchema,
				SecretStripper: ai.NewSecretStripper(),
			})
			if err != nil {
				return voiceEvaluationResult{}, fmt.Errorf("voice evaluation draft: %w", err)
			}
			var draft replyDraft
			if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &draft); err != nil || strings.TrimSpace(draft.Body) == "" {
				structuredValid = false
				drafts = append(drafts, voiceEvalDraft{prompt: prompt, score: 0})
				bodies = append(bodies, "")
				continue
			}
			sanitized := ai.SanitizeAIPatterns(draft.Body)
			hardFailures += len(ai.DetectAIPatterns(sanitized))
			drafts = append(drafts, voiceEvalDraft{prompt: prompt, subject: draft.Subject, body: sanitized})
			bodies = append(bodies, sanitized)
		}
		judgeScores, judgeValid, err := judgeVoiceDrafts(ctx, brain, sample.Text, bodies)
		if err != nil {
			return voiceEvaluationResult{}, err
		}
		if !judgeValid {
			// A judge that returned no usable verdict leaves the candidate
			// unscored on half its signal; that is invalid model output, and
			// an unscored candidate must not auto-activate.
			structuredValid = false
		}
		base := len(drafts) - len(bodies)
		for i, judged := range judgeScores {
			if drafts[base+i].body == "" {
				continue
			}
			proximity := stylometricProximity(artifact.Stats, drafts[base+i].body)
			drafts[base+i].score = round4(0.5*proximity + 0.5*judged)
		}
	}
	return scoreVoiceCandidate(artifact, drafts, hardFailures, structuredValid, predecessor), nil
}

// judgeVoiceDrafts scores one prompt's repeats against its held-out original
// in ONE call. A malformed judge answer scores neutrally at 0.5 AND reports
// valid=false, so the caller blocks auto-activation instead of letting the
// neutral fallback blend into a passing score.
func judgeVoiceDrafts(ctx context.Context, brain completer, original string, bodies []string) ([]float64, bool, error) {
	var payload strings.Builder
	payload.WriteString("<author_sample>\n" + original + "\n</author_sample>\n")
	for i, body := range bodies {
		fmt.Fprintf(&payload, "<draft index=\"%d\">\n%s\n</draft>\n", i+1, body)
	}
	resp, err := brain.Complete(ctx, model.Request{
		System:         voiceEvalJudgeSystem,
		Messages:       []model.Message{{Role: chatRoleUser, Content: payload.String()}},
		MaxTokens:      300,
		ResponseSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["scores"],"properties":{"scores":{"type":"array","items":{"type":"number","minimum":0,"maximum":1}}}}`),
		SecretStripper: ai.NewSecretStripper(),
	})
	if err != nil {
		return nil, false, fmt.Errorf("voice evaluation judge: %w", err)
	}
	var judged struct {
		Scores []float64 `json:"scores"`
	}
	scores := make([]float64, len(bodies))
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &judged); err != nil || len(judged.Scores) != len(bodies) {
		for i := range scores {
			scores[i] = 0.5
		}
		return scores, false, nil
	}
	for i := range scores {
		scores[i] = clamp01(judged.Scores[i])
	}
	return scores, true, nil
}

// stylometricProximity measures how close a draft's deterministic
// fingerprint sits to the corpus fingerprint over sentence rhythm and
// punctuation rates; 1 is indistinguishable, 0 is far off.
func stylometricProximity(corpus ai.VoiceStats, body string) float64 {
	draft := ai.AnalyzeVoice([]ai.VoiceSample{{ID: "draft", Register: "general", Text: body, WordCount: len(strings.Fields(body))}})
	distance := 0.0
	if corpus.MeanSentenceWords > 0 {
		distance += math.Abs(draft.MeanSentenceWords-corpus.MeanSentenceWords) / corpus.MeanSentenceWords
	}
	for _, pair := range [][2]float64{
		{draft.QuestionPer100Words, corpus.QuestionPer100Words},
		{draft.ExclaimPer100Words, corpus.ExclaimPer100Words},
		{draft.EmDashPer100Words, corpus.EmDashPer100Words},
	} {
		distance += math.Abs(pair[0]-pair[1]) / (pair[1] + 1)
	}
	return clamp01(1 / (1 + distance))
}
