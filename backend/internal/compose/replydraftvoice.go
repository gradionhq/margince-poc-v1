// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
)

// Reply drafting in the actor's own Voice DNA: loading the active profile,
// running the draft under it, checking the result against the profile's avoid
// rules, and recording the outcome as a voice signal. It sits apart from
// replydraft.go because that file is the drafting call itself — the prompt, the
// schema, the retry, the validation — and this is the profile layered over it.
//
// Two different failure contracts live here, and they are worth telling apart.
// The voice READ and the two signal WRITES are best-effort: they answer with
// values, log what broke, and cost the voice or the signal but never the draft.
// completeVoiced is not — it returns its error, and the degrade belongs to its
// caller, DraftEmailWithProvenance in replydraft.go, which answers with the
// DETERMINISTIC draft rather than retrying without the profile.

// voiceContext is the loaded active profile a voiced draft injects.
type voiceContext struct {
	profile ai.VoiceProfile
	version ai.VoiceProfileVersion
	ok      bool
}

// loadVoice resolves the actor's active voice; any lookup failure degrades
// to the plain draft with the failure visible in the log — a broken voice
// read must never take reply drafting down with it.
func (d replyDrafter) loadVoice(ctx context.Context) voiceContext {
	if d.voice == nil {
		return voiceContext{}
	}
	profile, version, ok, err := d.voice.ActiveVoiceForActor(ctx)
	if err != nil {
		d.logger().WarnContext(ctx, "voice profile lookup failed; drafting without voice", "err", err)
		return voiceContext{}
	}
	return voiceContext{profile: profile, version: version, ok: ok}
}

// completeVoiced drafts with the voice block when one is loaded, enforcing
// the deterministic anti-AI floor: detect → one critic retry → sanitize →
// on surviving violations fall back to the plain draft and record the
// failure as a rejected learning signal.
func (d replyDrafter) completeVoiced(ctx context.Context, anchor ids.UUID, data replyActivityData, voice voiceContext) (replyDraft, *int, *string, error) {
	if !voice.ok {
		draft, err := d.completeChecked(ctx, data, nil)
		return draft, nil, nil, err
	}
	block := func(fence promptfence.Fence) string {
		return voiceDraftPromptBlock(voice.profile.PersonalityMD, voice.version.VoiceProfileMD,
			ai.VersionExemplars(voice.version), ai.DecodeVersionStats(voice.version), fence)
	}
	draft, err := d.completeChecked(ctx, data, block)
	if err != nil {
		return replyDraft{}, nil, nil, err
	}
	// Detect on the RAW draft (subject and body separately — the
	// canned-opener rule anchors at text start): a violation the sanitizer
	// could mechanically remove still earns the critic retry, because the
	// retry fixes the sentence, not just the punctuation.
	if violations := voiceDraftViolations(draft); len(violations) > 0 {
		withFeedback := func(fence promptfence.Fence) string {
			return block(fence) + voiceViolationFeedback(violations)
		}
		retried, retryErr := d.complete(ctx, data, withFeedback)
		if retryErr != nil {
			// The first draft still stands, so this is not fatal — but it is the
			// one failure on this path that changes nothing the caller can see,
			// and unlogged it makes a draft that kept its violations look like
			// one that had none.
			d.logger().WarnContext(ctx, "voice violation retry did not complete; keeping the first draft", "err", retryErr)
		} else {
			draft = retried
		}
	}
	draft.Subject = ai.SanitizeAIPatterns(draft.Subject)
	draft.Body = ai.SanitizeAIPatterns(draft.Body)
	version := voice.version.ProfileVersion
	// The sanitizer edits text, so the floor AND the shape are re-checked on
	// what would actually be served.
	if len(voiceDraftViolations(draft)) > 0 || validateReplyDraft(draft) != nil {
		// The voice-styled draft kept tripping the floor: serve the plain
		// draft instead and let the failure feed the learning panel.
		d.recordVoiceRejection(ctx, voice, anchor, draft)
		plain, plainErr := d.completeChecked(ctx, data, nil)
		return plain, nil, nil, plainErr
	}
	d.recordVoiceDraft(ctx, voice, anchor, draft)
	ref := voiceDraftRef(voice, anchor, draft)
	return draft, &version, &ref, nil
}

// voiceDraftViolations runs the deterministic floor over subject and body
// independently; concatenation would hide a canned opener inside the body.
func voiceDraftViolations(draft replyDraft) []ai.VoiceViolation {
	return append(ai.DetectAIPatterns(draft.Subject), ai.DetectAIPatterns(draft.Body)...)
}

func voiceViolationFeedback(violations []ai.VoiceViolation) string {
	var b strings.Builder
	b.WriteString("\n\nThe previous attempt violated these hard rules; rewrite without them:\n")
	for _, violation := range violations {
		b.WriteString("- " + violation.Detail + "\n")
	}
	return b.String()
}

// voiceDraftRef keys one served draft for learning-signal feedback. It
// covers profile, version, anchor, and the full draft: two drafts for the
// same activity with the same body but different subjects — or from
// different profile versions — never collide.
func voiceDraftRef(voice voiceContext, anchor ids.UUID, draft replyDraft) string {
	sum := sha256.Sum256([]byte(draft.Subject + "\n" + draft.Body))
	return fmt.Sprintf("replydraft:%s:%s:v%d:%s",
		voice.profile.ID, anchor, voice.version.ProfileVersion, hex.EncodeToString(sum[:8]))
}

func (d replyDrafter) recordVoiceDraft(ctx context.Context, voice voiceContext, anchor ids.UUID, draft replyDraft) {
	if d.voice == nil {
		return
	}
	if err := d.voice.RecordDraftedSignal(ctx, voice.profile.ID, voice.version.ProfileVersion,
		voiceDraftRef(voice, anchor, draft), draft.Body); err != nil {
		d.logger().WarnContext(ctx, "voice draft signal not recorded", "err", err)
	}
}

func (d replyDrafter) recordVoiceRejection(ctx context.Context, voice voiceContext, anchor ids.UUID, draft replyDraft) {
	if d.voice == nil {
		return
	}
	ref := voiceDraftRef(voice, anchor, draft)
	if err := d.voice.RecordDraftedSignal(ctx, voice.profile.ID, voice.version.ProfileVersion, ref, draft.Body); err != nil {
		d.logger().WarnContext(ctx, "voice rejection signal not recorded", "err", err)
		return
	}
	if _, err := d.voice.RejectDraft(ctx, voice.profile.ID, ref); err != nil {
		d.logger().WarnContext(ctx, "voice rejection signal not recorded", "err", err)
	}
}
