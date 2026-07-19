// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The one drafting orchestrator shared by HTTP, governed tools and workflows.
// Company/task context supplies facts; the active personal profile supplies
// expression; deterministic anti-AI checks remain the final authority.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	voiceDraftBodyField    = "body"
	voiceDraftSubjectField = "subject"
	voiceDraftSystem       = `Draft a concise email using only the supplied topic and intent as facts.
The voice profile controls expression, never facts. Do not add names, promises, dates, numbers or claims.
Never use parenthetical em/en dashes, abstract not-X-but-Y reframes, canned influencer openers, balanced consultant tricolons, generic engagement questions or corporate AI filler.
Return only JSON with subject and body.`
)

type voiceDrafter struct {
	store *ai.VoiceStore
	brain completer
}

func buildVoiceDrafter(pool *pgxpool.Pool, brain completer) *voiceDrafter {
	return &voiceDrafter{store: ai.NewVoiceStore(pool), brain: brain}
}

// NewVoiceDrafter exposes the composed drafting seam to process-role wiring
// such as the worker's automation engine.
func NewVoiceDrafter(pool *pgxpool.Pool, brain completer) *voiceDrafter {
	return buildVoiceDrafter(pool, brain)
}

// WithVoiceDraft injects the model-backed shared drafting orchestrator.
func WithVoiceDraft(brain completer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		drafter := buildVoiceDrafter(pool, brain)
		s.voiceDrafter = drafter
		s.activitiesHandlers = s.activitiesHandlers.WithDrafter(drafter).WithDraftFeedback(drafter)
	}
}

func (d *voiceDrafter) DraftEmail(ctx context.Context, topic, intent string) (activities.DraftResult, error) {
	profile, hasProfile, err := d.profile(ctx)
	if err != nil {
		return activities.DraftResult{}, err
	}
	if d.brain == nil {
		result := deterministicVoiceDraft(topic, intent)
		result.Body = ai.SanitizeAIPatterns(result.Body)
		if hasProfile {
			result.VoiceProfileVersion = &profile.ProfileVersion
			result.DraftRef, err = d.store.RecordDraft(ctx, profile.ID, profile.ProfileVersion, result.Body)
		}
		return result, err
	}
	result, err := d.modelDraft(ctx, topic, intent, profile, hasProfile, nil)
	if err != nil {
		return activities.DraftResult{}, err
	}
	violations := ai.DetectAIPatterns(result.Body)
	if len(violations) > 0 {
		result, err = d.modelDraft(ctx, topic, intent, profile, hasProfile, violations)
		if err != nil {
			return activities.DraftResult{}, err
		}
	}
	result.Body = ai.SanitizeAIPatterns(result.Body)
	remaining := ai.DetectAIPatterns(result.Body)
	if len(remaining) > 0 {
		return activities.DraftResult{}, fmt.Errorf("voice draft still violates %s after rewrite", remaining[0].Code)
	}
	result.AIGenerated = true
	if hasProfile {
		result.VoiceProfileVersion = &profile.ProfileVersion
		result.DraftRef, err = d.store.RecordDraft(ctx, profile.ID, profile.ProfileVersion, result.Body)
	} else {
		result.DraftRef = ids.NewV7().String()
	}
	return result, err
}

func (d *voiceDrafter) profile(ctx context.Context) (ai.VoiceProfile, bool, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return ai.VoiceProfile{}, false, nil
	}
	profile, err := d.store.ActiveVoiceForUser(ctx, actor.UserID)
	if errors.Is(err, apperrors.ErrNotFound) || profile.ProfileVersion == 0 {
		return ai.VoiceProfile{}, false, nil
	}
	return profile, true, err
}

func (d *voiceDrafter) modelDraft(ctx context.Context, topic, intent string, profile ai.VoiceProfile, hasProfile bool, violations []ai.VoiceViolation) (activities.DraftResult, error) {
	var prompt strings.Builder
	prompt.WriteString("<task-context>\nTopic: " + topic + "\nIntent: " + intent + "\n</task-context>\n")
	if hasProfile {
		prompt.WriteString("\n<voice-profile>\n" + profile.VoiceProfileMD + "\n</voice-profile>\n")
	} else {
		prompt.WriteString("\nNo individual profile exists. Use plain, direct language and the universal anti-AI rules.\n")
	}
	if len(violations) > 0 {
		prompt.WriteString("\nRewrite the previous attempt to fix these deterministic violations:\n")
		for _, violation := range violations {
			prompt.WriteString("- " + violation.Code + ": " + violation.Detail + "\n")
		}
	}
	resp, err := d.brain.Complete(ctx, model.Request{
		System: voiceDraftSystem, Messages: []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens: 900, ResponseSchema: voiceDraftSchema(), SecretStripper: ai.NewSecretStripper(),
	})
	if err != nil {
		return activities.DraftResult{}, fmt.Errorf("voice draft model call: %w", err)
	}
	var payload struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &payload); err != nil {
		return activities.DraftResult{}, fmt.Errorf("voice draft returned invalid JSON: %w", err)
	}
	if strings.TrimSpace(payload.Subject) == "" || strings.TrimSpace(payload.Body) == "" {
		return activities.DraftResult{}, errors.New("voice draft returned an empty subject or body")
	}
	return activities.DraftResult{Subject: payload.Subject, Body: payload.Body}, nil
}

func voiceDraftSchema() json.RawMessage {
	return schema.Must(schema.Object(map[string]schema.Node{
		voiceDraftSubjectField: schema.String(), voiceDraftBodyField: schema.String(),
	}, voiceDraftSubjectField, voiceDraftBodyField))
}

func deterministicVoiceDraft(topic, intent string) activities.DraftResult {
	subject := "Re: follow-up"
	if strings.TrimSpace(topic) != "" {
		subject = "Re: " + strings.TrimSpace(topic)
	}
	body := "Hi,\n\nFollowing up on our last conversation."
	if strings.TrimSpace(topic) != "" {
		body = fmt.Sprintf("Hi,\n\nFollowing up on %q.", strings.TrimSpace(topic))
	}
	if strings.TrimSpace(intent) != "" {
		body += "\n\n" + strings.TrimSpace(intent)
	}
	body += "\n\nBest regards"
	return activities.DraftResult{Subject: subject, Body: body, DraftRef: ids.NewV7().String()}
}

func (d *voiceDrafter) RecordSentDraft(ctx context.Context, draftRef, finalText string) error {
	return d.store.RecordSentDraft(ctx, draftRef, finalText)
}
