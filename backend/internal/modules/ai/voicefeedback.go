// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Draft feedback records the protected original and the user's final sent
// version. Only significant edits become positive learning evidence; one-off
// transformations remain signals until the builder sees repetition.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func stableTransformations(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

// RecordDraft protects the original generated text for later edit comparison.
func (s *VoiceStore) RecordDraft(ctx context.Context, profileID ids.UUID, profileVersion int, original string) (string, error) {
	ref := ids.NewV7().String()
	if profileVersion < 1 || strings.TrimSpace(original) == "" {
		return ref, nil
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		var signalID ids.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO voice_learning_signal
			  (workspace_id, voice_profile_id, draft_ref, profile_version, outcome, original_text)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, 'drafted', $4)
			RETURNING id`, profileID, ref, profileVersion, original).Scan(&signalID); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "voice_learning_signal", signalID, nil,
			map[string]any{voiceFieldProfileID: profileID, "draft_ref": ref, voiceFieldProfileVersion: profileVersion, voiceFieldOutcome: voiceOutcomeDrafted})
		return err
	})
	return ref, err
}

// RecordSentDraft turns a materially edited sent draft into a learning signal.
func (s *VoiceStore) RecordSentDraft(ctx context.Context, draftRef, finalText string) error {
	if strings.TrimSpace(draftRef) == "" {
		return nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return apperrors.ErrPermissionDenied
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var signalID ids.UUID
		var original, outcome string
		err := tx.QueryRow(ctx, `
			SELECT s.id, s.original_text, s.outcome
			FROM voice_learning_signal s
			JOIN voice_profile p ON p.id = s.voice_profile_id
			WHERE s.draft_ref = $1 AND p.owner_id = $2 AND p.archived_at IS NULL
			FOR UPDATE OF s`, draftRef, actor.UserID).Scan(&signalID, &original, &outcome)
		if errors.Is(err, pgx.ErrNoRows) {
			// A baseline-only draft has no profile signal row. Sending it is
			// still valid and there is simply nothing personal to learn from.
			return nil
		}
		if err != nil {
			return err
		}
		if outcome != voiceOutcomeDrafted {
			return nil
		}
		similarity := wordSetSimilarity(original, finalText)
		newOutcome := "accepted"
		if similarity < 0.95 {
			newOutcome = "edited_sent"
		}
		transforms, err := json.Marshal(draftTransformations(original, finalText))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE voice_learning_signal SET outcome = $2, final_text = $3, similarity = $4, transformations = $5
			WHERE id = $1`, signalID, newOutcome, finalText, similarity, transforms); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "voice_learning_signal", signalID,
			map[string]any{voiceFieldOutcome: voiceOutcomeDrafted}, map[string]any{voiceFieldOutcome: newOutcome, "similarity": similarity})
		return err
	})
}

func wordSetSimilarity(left, right string) float64 {
	leftSet := normalizedWordSet(left)
	rightSet := normalizedWordSet(right)
	if len(leftSet) == 0 && len(rightSet) == 0 {
		return 1
	}
	intersection := 0
	union := map[string]bool{}
	for word := range leftSet {
		union[word] = true
		if rightSet[word] {
			intersection++
		}
	}
	for word := range rightSet {
		union[word] = true
	}
	return float64(intersection) / float64(len(union))
}

func normalizedWordSet(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.Fields(text) {
		if normalized := normalizeStyleWord(word); normalized != "" {
			words[normalized] = true
		}
	}
	return words
}

func draftTransformations(original, final string) []string {
	var transforms []string
	if strings.ContainsAny(original, "—–") && !strings.ContainsAny(final, "—–") {
		transforms = append(transforms, "removed parenthetical dashes")
	}
	leftWords, rightWords := WordCount(original), WordCount(final)
	if rightWords*4 < leftWords*3 {
		transforms = append(transforms, "made the draft substantially shorter")
	}
	if leftWords*4 < rightWords*3 {
		transforms = append(transforms, "added substantial personal context")
	}
	if firstLine(original) != firstLine(final) {
		transforms = append(transforms, "rewrote the opening")
	}
	return stableTransformations(transforms)
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.ToLower(strings.TrimSpace(line))
}
