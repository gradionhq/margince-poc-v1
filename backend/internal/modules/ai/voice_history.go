// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VoiceProfileDelta records the classified change between two immutable profile versions.
type VoiceProfileDelta struct {
	ID                   ids.UUID
	ProfileID            ids.UUID
	FromVersion          *int
	ToVersion            int
	Classification       string
	ActivationOutcome    string
	WordsAdded           int
	SourcesAdded         int
	SourcesExcluded      int
	IdentityWordJaccard  *float64
	SignatureSetJaccard  *float64
	AvoidRulesAdded      int
	AvoidRulesRemoved    int
	RegisterRulesRemoved int
	CreatedAt            time.Time
	UpdatedAt            *time.Time
	ArchivedAt           *time.Time
}

// VoiceProfileDeltaPage is one keyset-paginated slice of profile history.
type VoiceProfileDeltaPage struct {
	Items      []VoiceProfileDelta
	NextCursor string
	HasMore    bool
}

func scanVoiceDelta(row pgx.Row) (VoiceProfileDelta, error) {
	var (
		delta VoiceProfileDelta
		raw   []byte
	)
	err := row.Scan(&delta.ID, &delta.ProfileID, &delta.FromVersion, &delta.ToVersion,
		&delta.Classification, &delta.ActivationOutcome, &raw, &delta.CreatedAt,
		&delta.UpdatedAt, &delta.ArchivedAt)
	if err != nil {
		return VoiceProfileDelta{}, err
	}
	var values struct {
		WordsAdded           int      `json:"words_added"`
		SourcesAdded         int      `json:"sources_added"`
		SourcesExcluded      int      `json:"sources_excluded"`
		IdentityWordJaccard  *float64 `json:"identity_word_jaccard"`
		SignatureSetJaccard  *float64 `json:"signature_set_jaccard"`
		AvoidRulesAdded      int      `json:"avoid_rules_added"`
		AvoidRulesRemoved    int      `json:"avoid_rules_removed"`
		RegisterRulesRemoved int      `json:"register_rules_removed"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return VoiceProfileDelta{}, fmt.Errorf("decode stored voice delta: %w", err)
	}
	delta.WordsAdded = values.WordsAdded
	delta.SourcesAdded = values.SourcesAdded
	delta.SourcesExcluded = values.SourcesExcluded
	delta.IdentityWordJaccard = values.IdentityWordJaccard
	delta.SignatureSetJaccard = values.SignatureSetJaccard
	delta.AvoidRulesAdded = values.AvoidRulesAdded
	delta.AvoidRulesRemoved = values.AvoidRulesRemoved
	delta.RegisterRulesRemoved = values.RegisterRulesRemoved
	return delta, nil
}

// ListDeltas returns owner-visible profile changes in newest-first order.
func (s *VoiceStore) ListDeltas(ctx context.Context, profileID ids.UUID, cursor *string, limit *int) (VoiceProfileDeltaPage, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfileDeltaPage{}, err
	}
	n := storekit.ClampLimit(limit)
	args := []any{profileID}
	where := "voice_profile_id = $1 AND archived_at IS NULL"
	if cursor != nil && *cursor != "" {
		decoded, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return VoiceProfileDeltaPage{}, err
		}
		args = append(args, decoded.CreatedAt, decoded.ID)
		where += " AND (created_at, id) < ($2, $3)"
	}
	var page VoiceProfileDeltaPage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(`
			SELECT id, voice_profile_id, from_version, to_version, classification,
			       activation_outcome, delta_json, created_at, updated_at, archived_at
			FROM voice_profile_delta WHERE %s
			ORDER BY created_at DESC, id DESC LIMIT %d`, where, n+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanVoiceDelta(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return VoiceProfileDeltaPage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}

// VoiceLearningTransformation summarizes one repeatedly observed edit pattern.
type VoiceLearningTransformation struct {
	Key              string
	ObservationCount int
	Description      string
}

// VoiceLearningSummary reports draft outcomes and transformations eligible for learning.
type VoiceLearningSummary struct {
	Drafted               int
	Accepted              int
	EditedSent            int
	Rejected              int
	QualifyingSourceCount int
	QualifyingWords       int
	Transformations       []VoiceLearningTransformation
}

// LearningSummary derives the owner-visible learning state from durable signals.
func (s *VoiceStore) LearningSummary(ctx context.Context, profileID ids.UUID) (VoiceLearningSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceLearningSummary{}, err
	}
	var summary VoiceLearningSummary
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE outcome = 'drafted')::int,
			       count(*) FILTER (WHERE outcome = 'accepted')::int,
			       count(*) FILTER (WHERE outcome = 'edited_sent')::int,
			       count(*) FILTER (WHERE outcome = 'rejected')::int
			FROM voice_learning_signal
			WHERE voice_profile_id = $1 AND archived_at IS NULL`, profileID).Scan(
			&summary.Drafted, &summary.Accepted, &summary.EditedSent, &summary.Rejected); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int, coalesce(sum(word_count), 0)::int
			FROM voice_corpus_source
			WHERE voice_profile_id = $1 AND origin = 'draft_signal' AND NOT excluded
			  AND archived_at IS NULL AND content_erased_at IS NULL`, profileID).Scan(
			&summary.QualifyingSourceCount, &summary.QualifyingWords); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT item->>'key', count(*)::int, min(item->>'description')
			FROM voice_learning_signal
			CROSS JOIN LATERAL jsonb_array_elements(transformations) AS item
			WHERE voice_profile_id = $1 AND outcome = 'edited_sent'
			  AND qualifies_as_source AND archived_at IS NULL
			GROUP BY item->>'key'
			HAVING count(*) >= 5
			ORDER BY item->>'key'`, profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var transformation VoiceLearningTransformation
			if err := rows.Scan(&transformation.Key, &transformation.ObservationCount, &transformation.Description); err != nil {
				return err
			}
			summary.Transformations = append(summary.Transformations, transformation)
		}
		return rows.Err()
	})
	if summary.Transformations == nil {
		summary.Transformations = []VoiceLearningTransformation{}
	}
	return summary, err
}

// RejectDraft records rejection of a still-pending draft without retaining its plaintext reference.
func (s *VoiceStore) RejectDraft(ctx context.Context, profileID ids.UUID, draftRef string) (VoiceLearningSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceLearningSummary{}, err
	}
	if draftRef == "" {
		return VoiceLearningSummary{}, &CorpusIngestError{Field: voiceKeyDraftRef, Reason: voiceValidationNotEmpty}
	}
	hash := sha256.Sum256([]byte(draftRef))
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		var (
			signalID ids.UUID
			outcome  string
		)
		err := tx.QueryRow(ctx, `
			SELECT id, outcome FROM voice_learning_signal
			WHERE voice_profile_id = $1 AND draft_ref_hash = $2 AND archived_at IS NULL
			FOR UPDATE`, profileID, hash[:]).Scan(&signalID, &outcome)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if outcome == voiceOutcomeRejected {
			return nil
		}
		if outcome != voiceOutcomeDrafted {
			return fmt.Errorf("%w: a draft with outcome %s cannot be rejected", apperrors.ErrConflict, outcome)
		}
		err = tx.QueryRow(ctx, `
			UPDATE voice_learning_signal
			SET outcome = 'rejected', version = version + 1, updated_at = $3
			WHERE voice_profile_id = $1 AND draft_ref_hash = $2 AND outcome = 'drafted' AND archived_at IS NULL
			RETURNING id`, profileID, hash[:], s.now().UTC()).Scan(&signalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "voice_learning_signal", signalID,
			map[string]any{voiceKeyOutcome: voiceOutcomeDrafted}, map[string]any{voiceKeyOutcome: voiceOutcomeRejected})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "voice.draft_outcome_recorded", "voice_profile", profileID, map[string]any{
			voiceKeyProfileID: profileID, voiceKeyOutcome: voiceOutcomeRejected, "qualifies_as_source": false,
			"transformation_count": 0,
		})
	})
	if err != nil {
		return VoiceLearningSummary{}, err
	}
	return s.LearningSummary(ctx, profileID)
}
