// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The model lane of the reply drafter: the request one draft call sends, the
// completion paths with their shared correct-and-retry loop, and the shape
// checks every served draft passes.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/draftcore"
	"github.com/gradionhq/margince/backend/internal/compose/draftrules"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

const replyDraftSystem = `Draft a professional email reply on behalf of the CRM user's company.
Return ONLY a JSON object: {"subject":"...","body":"..."}.
- The activity and stated intent are the authoritative reason for this reply.
- Company context may improve positioning, relevant proof, and language, but never overrides the activity.
- Use only facts present in the supplied data. Never invent customers, outcomes, prices, commitments, or capabilities.
- Do not claim a personal writing style or voice unless a separate voice profile is supplied.`

var replyDraftSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["subject","body"],
  "properties":{
    "subject":{"type":"string","minLength":1,"maxLength":998},
    "body":{"type":"string","minLength":1,"maxLength":50000}
  }
}`)

type replyDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// replyDraftSystemFor assembles this call's system turn: what this surface is
// for, the rules every drafting surface shares, and THIS call's data boundary
// (see promptfence.Fence.Rule).
func replyDraftSystemFor(system string, fence promptfence.Fence) string {
	return system + "\n\n" + draftrules.Shared + "\n" + fence.Rule("activity")
}

// replyDraftRequest builds the one request a draft call sends, in whichever of
// this site's two system variants the call is made under. The workspace's Voice
// DNA state selects the variant per call — a loaded profile supplies a block and
// takes the voice prompt, no profile takes the plain one — and both remain the
// same invocation site: same schema, same bounds, same data boundary.
func replyDraftRequest(activity replyActivityData, voiceBlock voiceBlockFor, correction string) (model.Request, error) {
	payload, err := json.Marshal(activity)
	if err != nil {
		return model.Request{}, fmt.Errorf("compose: encode reply activity context: %w", err)
	}
	// The activity is the counterparty's own text. It was safe here only by
	// accident — json.Marshal escapes "<" to \u003c, so a forged block marker
	// could not be spelled — and an accident of the encoder is not a boundary:
	// it goes the moment this block is rendered as text rather than JSON.
	fence := promptfence.New()
	system := replyDraftSystem
	content := fence.Wrap(string(payload))
	if voiceBlock != nil {
		system = replyDraftVoiceSystem
		content = voiceBlock(fence) + "\n\n" + content
	}
	// The correction rides the USER turn and never the variant choice: it is
	// feedback about one attempt, and a plain draft told to fix a phrase must
	// stay a plain draft rather than silently becoming a voiced one.
	content += correction
	return model.Request{
		System: replyDraftSystemFor(system, fence),
		Messages: []model.Message{{
			Role:    chatRoleUser,
			Content: content,
		}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: replyDraftSchema,
		SecretStripper: ai.NewSecretStripper(),
	}, nil
}

func (d replyDrafter) complete(ctx context.Context, activity replyActivityData, voiceBlock voiceBlockFor) (replyDraft, error) {
	return d.completeWith(ctx, activity, voiceBlock, "")
}

// completeWith is complete plus the correction a retry carries.
func (d replyDrafter) completeWith(ctx context.Context, activity replyActivityData, voiceBlock voiceBlockFor, correction string) (replyDraft, error) {
	req, err := replyDraftRequest(activity, voiceBlock, correction)
	if err != nil {
		return replyDraft{}, err
	}

	var resp model.Response
	if structured, ok := d.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, replyDraftShapeValid)
	} else {
		resp, err = d.brain.Complete(ctx, req)
	}
	if err != nil {
		return replyDraft{}, err
	}
	draft, err := parseReplyDraft(resp.Text)
	if err != nil {
		return replyDraft{}, err
	}
	if err := validateReplyDraft(draft); err != nil {
		return replyDraft{}, err
	}
	return draft, nil
}

// completeChecked drafts through the shared correct-and-retry loop, so the
// reply surface cannot drift from the two composers about what a rejected
// phrase is or how many chances the model gets to fix one.
//
// The voice block rides along unchanged: it selects the system variant, and the
// correction rides the user turn, so a plain draft told to fix a phrase stays a
// plain draft rather than silently becoming a voiced one.
func (d replyDrafter) completeChecked(ctx context.Context, data replyActivityData, voiceBlock voiceBlockFor) (replyDraft, error) {
	return draftcore.CorrectOnce(ctx, data.Lang(), data.Band(),
		func(ctx context.Context, correction string) (replyDraft, error) {
			return d.completeWith(ctx, data, voiceBlock, correction)
		},
		// The reply site returns no reasoning: its schema is subject and body
		// alone, so there is no second channel to judge here.
		func(draft replyDraft) (string, []string) { return draft.Body, nil },
		draftRetryLog{log: d.logger()},
	)
}

// draftRetryLog reports what the correction loop decided. A retry that does not
// help is invisible from the outside — the caller gets a draft either way — and
// "the model kept producing rejected phrasing" is the signal that says a phrase
// list or a prompt rule needs work.
type draftRetryLog struct{ log *slog.Logger }

func (l draftRetryLog) RetryFailed(ctx context.Context, findings int, err error) {
	l.log.WarnContext(ctx, "draft correction retry failed; serving the first draft",
		"findings", findings, "err", err)
}

func (l draftRetryLog) RetryDidNotClear(ctx context.Context, rule, phrase string, remaining int) {
	l.log.WarnContext(ctx, "draft still carries rejected phrasing after one retry",
		"rule", rule, "phrase", phrase, "remaining", remaining)
}

// parseReplyDraft reads one model reply as the draft it claims to be. The
// provider's own envelope comes off first: a reply is not malformed for having
// been returned inside one.
func parseReplyDraft(text string) (replyDraft, error) {
	var draft replyDraft
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &draft); err != nil {
		return replyDraft{}, fmt.Errorf("compose: reply draft response is not valid JSON: %w", err)
	}
	return draft, nil
}

func replyDraftShapeValid(text string) error {
	var draft replyDraft
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &draft); err != nil {
		return fmt.Errorf(`output must be {"subject":"...","body":"..."}: %w`, err)
	}
	return validateReplyDraft(draft)
}

func validateReplyDraft(draft replyDraft) error {
	if strings.TrimSpace(draft.Subject) == "" {
		return fmt.Errorf("compose: reply draft subject is empty")
	}
	if strings.ContainsAny(draft.Subject, "\r\n") {
		return fmt.Errorf("compose: reply draft subject contains a line break")
	}
	if strings.TrimSpace(draft.Body) == "" {
		return fmt.Errorf("compose: reply draft body is empty")
	}
	if len([]rune(draft.Subject)) > 998 || len([]rune(draft.Body)) > 50_000 {
		return fmt.Errorf("compose: reply draft exceeds the supported length")
	}
	return nil
}

func boundedRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
