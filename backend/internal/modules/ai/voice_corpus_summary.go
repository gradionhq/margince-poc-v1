// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// corpusSummary computes the meter over the non-excluded manifest.
func corpusSummary(ctx context.Context, tx pgx.Tx, profileID ids.UUID) (CorpusSummary, error) {
	summary := CorpusSummary{
		TargetWords:   CorpusTargetWords,
		RegisterWords: map[string]int{},
	}
	rows, err := tx.Query(ctx, `
		SELECT register, sum(word_count)::int, count(*)::int
		FROM voice_corpus_source
		WHERE voice_profile_id = $1 AND NOT excluded AND archived_at IS NULL AND content_erased_at IS NULL
		GROUP BY register`, profileID)
	if err != nil {
		return CorpusSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			register string
			words    int
			count    int
		)
		if err := rows.Scan(&register, &words, &count); err != nil {
			return CorpusSummary{}, err
		}
		summary.RegisterWords[register] = words
		summary.TotalWords += words
		summary.SourceCount += count
	}
	if err := rows.Err(); err != nil {
		return CorpusSummary{}, err
	}
	summary.QualityBand = QualityBand(summary.TotalWords)
	summary.Maturity = Maturity(summary.TotalWords)
	return summary, nil
}

func corpusSourceHash(ctx context.Context, tx pgx.Tx, profileID ids.UUID) (string, error) {
	var hash string
	err := tx.QueryRow(ctx, `
		SELECT md5(coalesce(string_agg(content_hash, ',' ORDER BY source_ref), ''))
		FROM voice_corpus_source
		WHERE voice_profile_id = $1 AND NOT excluded AND archived_at IS NULL AND content_erased_at IS NULL`,
		profileID).Scan(&hash)
	return hash, err
}

// ProfilePresentation derives the two honest corpus axes and the pending
// candidate pointer without materializing them on the control row. The owner
// predicate is repeated even for an archived profile returned by DELETE.
func (s *VoiceStore) ProfilePresentation(ctx context.Context, profileID ids.UUID) (CorpusSummary, *int, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return CorpusSummary{}, nil, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return CorpusSummary{}, nil, apperrors.ErrPermissionDenied
	}
	var (
		summary          CorpusSummary
		candidateVersion *int
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM voice_profile
			  WHERE id = $1 AND scope = 'user' AND owner_id = $2
			)`, profileID, actor.UserID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperrors.ErrNotFound
		}
		var err error
		summary, err = corpusSummary(ctx, tx, profileID)
		if err != nil {
			return err
		}
		var candidate int
		err = tx.QueryRow(ctx, `
			SELECT profile_version
			FROM voice_profile_version
			WHERE voice_profile_id = $1 AND status = 'candidate' AND archived_at IS NULL
			ORDER BY profile_version DESC LIMIT 1`, profileID).Scan(&candidate)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		default:
			candidateVersion = &candidate
			return nil
		}
	})
	return summary, candidateVersion, err
}

func markProfileStale(ctx context.Context, tx pgx.Tx, profile VoiceProfile, now time.Time) error {
	if _, err := storekit.LockRow(ctx, tx, "voice_profile", profile.ID, storekit.LiveOnly); err != nil {
		return err
	}
	var (
		profileVersion int
		status         string
	)
	if err := tx.QueryRow(ctx, `
		SELECT profile_version, status FROM voice_profile WHERE id = $1`, profile.ID).Scan(&profileVersion, &status); err != nil {
		return err
	}
	if profileVersion == 0 || status == voiceProfileStatusStale {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE voice_profile
		SET status = $2, version = version + 1, updated_at = $3
		WHERE id = $1`, profile.ID, voiceProfileStatusStale, now)
	return err
}
