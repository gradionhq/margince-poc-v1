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
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
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

// voiceEvalDraftSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func voiceEvalDraftSystemFor(fence promptfence.Fence) string {
	return voiceEvalDraftSystem + "\n" + fence.Rule("profile and sample")
}

const voiceEvalJudgeSystem = `You compare drafts against a writing sample by the same author.
Score how convincingly each draft matches the author's voice: 1.0 reads like the author, 0.0 reads like generic AI writing.
Judge voice only — rhythm, vocabulary, directness, structure — never topic or factual overlap.
Return ONLY a JSON object: {"scores":[...]} with one number in [0,1] per draft, in order.`

// voiceEvalJudgeSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func voiceEvalJudgeSystemFor(fence promptfence.Fence) string {
	return voiceEvalJudgeSystem + "\n" + fence.Rule("author and draft")
}

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
	// Two passes: register diversity first, then fill the remaining slots —
	// a same-register tail must not leave reserved capacity unused.
	seenRegisters := map[string]bool{}
	held := map[string]bool{}
	for pass := 0; pass < 2 && len(heldOut) < maxHeld; pass++ {
		for _, sample := range ordered {
			if len(heldOut) == maxHeld {
				break
			}
			if held[sample.ID] || (pass == 0 && seenRegisters[sample.Register]) {
				continue
			}
			if buildWords-sample.WordCount < ai.StarterVoiceWords {
				continue
			}
			heldOut = append(heldOut, sample)
			held[sample.ID] = true
			seenRegisters[sample.Register] = true
			buildWords -= sample.WordCount
		}
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
//
// The fence must be the one the consuming call declares in its system prompt —
// this block is prepended to that call's user turn, not sent on its own.
func voiceDraftPromptBlock(personality, profileMD string, exemplars []ai.VoiceExemplar, stats ai.VoiceStats, fence promptfence.Fence) string {
	// Everything here descends from corpus text — the artifact carries verbatim
	// quotes, and exemplars ARE corpus excerpts — so each piece sits inside the
	// call's nonce boundary, unedited. The exemplars are shown to the model to be
	// imitated verbatim, which is exactly why they may not be rewritten on the
	// way in.
	var block strings.Builder
	block.WriteString("Voice profile:\n")
	if strings.TrimSpace(personality) != "" {
		block.WriteString("Human-authored identity (highest priority):\n" + fence.Wrap(strings.TrimSpace(personality)) + "\n\n")
	}
	block.WriteString(fence.Wrap(strings.TrimSpace(profileMD)))
	for _, exemplar := range exemplars {
		fmt.Fprintf(&block, "\n\nVerbatim example (%s %s):\n%s", exemplar.Register, exemplar.Kind, fence.Wrap(exemplar.Text))
	}
	fmt.Fprintf(&block, "\n\nStylometric guardrails — limits, NOT targets: mean sentence length ≈ %.0f words (do not write a wall of short sentences to hit it), em dashes per 100 words ≈ %.2f (at 0, treat them as forbidden).",
		stats.MeanSentenceWords, stats.EmDashPer100Words)
	return block.String()
}

// evalPromptFor derives one held-out drafting task: reply to the opening of
// the reserved sample in its register. This is the label the evaluation record
// keeps — plain text, no fence markers, because it is read by a human on the
// profile screen; evalTaskFor renders the same task for the model.
func evalPromptFor(sample ai.VoiceSample) string {
	return fmt.Sprintf("Reply briefly (register: %s) to this message from a colleague:\n%s",
		sample.Register, evalSampleOpening(sample))
}

// evalTaskFor is the model's copy of the task, with the corpus excerpt inside
// the call's boundary — the excerpt is the author's own mail, which routinely
// quotes what a counterparty wrote to them.
func evalTaskFor(sample ai.VoiceSample, fence promptfence.Fence) string {
	return fmt.Sprintf("Reply briefly (register: %s) to this message from a colleague:\n%s",
		sample.Register, fence.Wrap(evalSampleOpening(sample)))
}

// evalSampleOpening bounds the excerpt both renderings share.
func evalSampleOpening(sample ai.VoiceSample) string {
	words := strings.Fields(sample.Text)
	if len(words) > 40 {
		words = words[:40]
	}
	return strings.Join(words, " ")
}

type voiceEvalDraft struct {
	prompt  string
	subject string
	body    string
	score   float64
}

// voiceEvalDraftRequest is ONE held-out drafting call. The repeat index is part
// of the prompt: the same held-out sample is drafted voiceEvalRepeatsPerPrompt
// times, and a model asked the byte-identical question three times has every
// reason to answer it identically three times.
func voiceEvalDraftRequest(personality string, artifact ai.VoiceArtifact, sample ai.VoiceSample, repeat int) model.Request {
	// One fence per call, so the profile block and the excerpt are bounded by
	// the marker THIS call's system prompt names.
	fence := promptfence.New()
	profileBlock := voiceDraftPromptBlock(personality, artifact.Markdown, artifact.Exemplars, artifact.Stats, fence)
	return model.Request{
		System: voiceEvalDraftSystemFor(fence),
		Messages: []model.Message{{Role: chatRoleUser, Content: profileBlock + "\n\n" + evalTaskFor(sample, fence) +
			fmt.Sprintf("\n(variation %d)", repeat+1)}},
		MaxTokens:      1200,
		ResponseSchema: replyDraftSchema,
		SecretStripper: ai.NewSecretStripper(),
	}
}

// voiceEvalDraftReply is one held-out draft as the evaluation keeps it:
// sanitized, because sanitized is what is cached for the profile screen, and
// carrying the deterministic tells the anti-AI floor still finds in it.
type voiceEvalDraftReply struct {
	subject string
	body    string
	tells   []ai.VoiceViolation
}

// readVoiceEvalDraft is the evaluation's own reading of one draft reply. A
// draft it cannot read scores zero AND leaves the whole candidate structurally
// invalid, so the refusal is an error rather than a flag: there is no usable
// half of an unreadable draft.
//
// The tell floor covers the whole draft — a tell in the subject is as
// disqualifying as one in the body — and each half is checked SEPARATELY,
// because the canned-opener rule anchors at text start and a concatenation
// would hide a canned opener in the body.
func readVoiceEvalDraft(text string) (voiceEvalDraftReply, error) {
	var draft replyDraft
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &draft); err != nil {
		return voiceEvalDraftReply{}, fmt.Errorf(
			`compose: voice evaluation draft is not {"subject":"...","body":"..."}: %w`, err)
	}
	if strings.TrimSpace(draft.Subject) == "" || strings.TrimSpace(draft.Body) == "" {
		return voiceEvalDraftReply{}, errors.New("compose: voice evaluation draft has an empty subject or body")
	}
	subject := ai.SanitizeAIPatterns(draft.Subject)
	body := ai.SanitizeAIPatterns(draft.Body)
	return voiceEvalDraftReply{
		subject: subject,
		body:    body,
		tells:   append(ai.DetectAIPatterns(subject), ai.DetectAIPatterns(body)...),
	}, nil
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
	drafts := make([]voiceEvalDraft, 0, len(heldOut)*voiceEvalRepeatsPerPrompt)
	hardFailures := 0
	structuredValid := true
	for _, sample := range heldOut {
		prompt := evalPromptFor(sample)
		var bodies []string
		for repeat := 0; repeat < voiceEvalRepeatsPerPrompt; repeat++ {
			resp, err := brain.Complete(ctx, voiceEvalDraftRequest(personality, artifact, sample, repeat))
			if err != nil {
				return voiceEvaluationResult{}, fmt.Errorf("voice evaluation draft: %w", err)
			}
			reply, err := readVoiceEvalDraft(resp.Text)
			if err != nil {
				structuredValid = false
				drafts = append(drafts, voiceEvalDraft{prompt: prompt, score: 0})
				bodies = append(bodies, "")
				continue
			}
			hardFailures += len(reply.tells)
			drafts = append(drafts, voiceEvalDraft{prompt: prompt, subject: reply.subject, body: reply.body})
			bodies = append(bodies, reply.body)
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

// voiceEvalJudgeSchema bounds the judge to one number per draft in [0,1]. The
// count is not expressible here — a schema cannot see how many drafts this call
// carries — which is why readVoiceJudgeScores checks it.
var voiceEvalJudgeSchema = json.RawMessage(
	`{"type":"object","additionalProperties":false,"required":["scores"],` +
		`"properties":{"scores":{"type":"array","items":{"type":"number","minimum":0,"maximum":1}}}}`)

// voiceEvalJudgeRequest is ONE judging call: the held-out original and every
// repeat drafted against it, each inside the marker this call's system prompt
// names. Both sides are model-adjacent text — the original is the author's own
// mail and the drafts are model output — so neither is presented as
// instruction.
func voiceEvalJudgeRequest(original string, bodies []string) model.Request {
	fence := promptfence.New()
	var payload strings.Builder
	payload.WriteString("Author sample:\n" + fence.Wrap(original) + "\n")
	for i, body := range bodies {
		fmt.Fprintf(&payload, "Draft %d:\n%s\n", i+1, fence.Wrap(body))
	}
	return model.Request{
		System:         voiceEvalJudgeSystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: payload.String()}},
		MaxTokens:      300,
		ResponseSchema: voiceEvalJudgeSchema,
		SecretStripper: ai.NewSecretStripper(),
	}
}

// readVoiceJudgeScores is the evaluation's own reading of a judge answer: one
// score per draft, clamped to the range the prompt asked for.
//
// An answer it cannot read — or one that scores a different number of drafts
// than were judged, which leaves every score ambiguous about which draft it
// belongs to — is refused, and the neutral 0.5 per draft is returned beside the
// refusal. The caller keeps the neutral scores so a whole prompt is not lost,
// and blocks auto-activation on the refusal so the fallback never blends into a
// passing score.
func readVoiceJudgeScores(text string, want int) ([]float64, error) {
	var judged struct {
		Scores []float64 `json:"scores"`
	}
	neutral := make([]float64, want)
	for i := range neutral {
		neutral[i] = 0.5
	}
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &judged); err != nil {
		return neutral, fmt.Errorf(`compose: voice evaluation judge answer is not {"scores":[...]}: %w`, err)
	}
	if len(judged.Scores) != want {
		return neutral, fmt.Errorf("compose: voice evaluation judge scored %d drafts, and %d were judged",
			len(judged.Scores), want)
	}
	scores := make([]float64, want)
	for i := range scores {
		scores[i] = clamp01(judged.Scores[i])
	}
	return scores, nil
}

// judgeVoiceDrafts scores one prompt's repeats against its held-out original
// in ONE call. A refused answer is not a failed call: the neutral scores stand
// and the caller is told they are not a verdict.
func judgeVoiceDrafts(ctx context.Context, brain completer, original string, bodies []string) ([]float64, bool, error) {
	resp, err := brain.Complete(ctx, voiceEvalJudgeRequest(original, bodies))
	if err != nil {
		return nil, false, fmt.Errorf("voice evaluation judge: %w", err)
	}
	scores, refused := readVoiceJudgeScores(resp.Text, len(bodies))
	return scores, refused == nil, nil
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
