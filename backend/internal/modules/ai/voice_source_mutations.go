// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// UpdateSource flips a manifest row's inclusion or weight without rebuilding.
func (s *VoiceStore) UpdateSource(ctx context.Context, profileID, sourceID ids.UUID, in UpdateSourceInput) (VoiceCorpusSource, CorpusSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	excluded, err := validateSourceUpdate(in)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	var (
		source  VoiceCorpusSource
		summary CorpusSummary
	)
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		source, summary, err = s.updateVoiceSource(ctx, tx, profileID, sourceID, in, excluded)
		return err
	})
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	return source, summary, nil
}

func validateSourceUpdate(in UpdateSourceInput) (*bool, error) {
	if in.Weight != nil && (*in.Weight < 0 || *in.Weight > 2.0) {
		return nil, &CorpusIngestError{Field: voiceKeyWeight, Reason: "must be between 0 and 2"}
	}
	excluded := in.Excluded
	if in.Included != nil {
		value := !*in.Included
		excluded = &value
	}
	if excluded == nil && in.Weight == nil {
		return nil, &CorpusIngestError{Field: "body", Reason: "provide included or weight"}
	}
	return excluded, nil
}

func (s *VoiceStore) updateVoiceSource(ctx context.Context, tx pgx.Tx, profileID, sourceID ids.UUID, in UpdateSourceInput, excluded *bool) (VoiceCorpusSource, CorpusSummary, error) {
	profile, err := s.visibleProfile(ctx, tx, profileID)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	if err := ownerOnly(ctx, profile); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	before, err := scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM voice_corpus_source
			 WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL`,
		voiceSourceColumns), sourceID, profileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceCorpusSource{}, CorpusSummary{}, apperrors.ErrNotFound
	}
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	if in.IfVersion != nil && *in.IfVersion != before.Version {
		return VoiceCorpusSource{}, CorpusSummary{}, apperrors.ErrVersionSkew
	}
	source, err := scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_corpus_source SET
			  excluded = coalesce($3, excluded),
			  exclusion_reason = CASE
			    WHEN coalesce($3, excluded) THEN coalesce(exclusion_reason, 'owner_excluded')
			    ELSE NULL
			  END,
			  weight = coalesce($4, weight),
			  version = version + 1,
			  updated_at = $5
			WHERE id = $1 AND voice_profile_id = $2
			RETURNING %s`, voiceSourceColumns),
		sourceID, profileID, excluded, in.Weight, s.now().UTC()))
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	summary, err := s.recordSourceUpdate(ctx, tx, profile, before, source)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	return source, summary, nil
}

func (s *VoiceStore) recordSourceUpdate(ctx context.Context, tx pgx.Tx, profile VoiceProfile, before, source VoiceCorpusSource) (CorpusSummary, error) {
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_corpus_source", source.ID,
		map[string]any{voiceKeyExcluded: before.Excluded, voiceKeyWeight: before.Weight},
		map[string]any{voiceKeyExcluded: source.Excluded, voiceKeyWeight: source.Weight})
	if err != nil {
		return CorpusSummary{}, err
	}
	summary, err := corpusSummary(ctx, tx, profile.ID)
	if err != nil {
		return CorpusSummary{}, err
	}
	sourceHash, err := corpusSourceHash(ctx, tx, profile.ID)
	if err != nil {
		return CorpusSummary{}, err
	}
	if err := markProfileStale(ctx, tx, profile, s.now().UTC()); err != nil {
		return CorpusSummary{}, err
	}
	action, wordDelta := sourceInclusionChange(before, source)
	err = storekit.Emit(ctx, tx, auditID, "voice.corpus_changed", "voice_profile", profile.ID, map[string]any{
		voiceKeyProfileID: profile.ID, voiceKeySourceID: source.ID, voiceKeyAction: action,
		voiceKeyOrigin: source.Origin, voiceKeyRegister: source.Register, voiceKeyWordDelta: wordDelta,
		voiceKeySourceCount: summary.SourceCount, voiceKeySourceHash: sourceHash,
	})
	if err != nil {
		return CorpusSummary{}, err
	}
	return summary, nil
}

func sourceInclusionChange(before, source VoiceCorpusSource) (string, int) {
	if source.Excluded {
		return "excluded", -source.WordCount
	}
	if before.Excluded == source.Excluded {
		return voiceKeyIncluded, 0
	}
	return voiceKeyIncluded, source.WordCount
}

// DeleteSource scrubs retained text and archives the manifest row. The row
// remains as an auditable exclusion fact but can no longer feed a build.
func (s *VoiceStore) DeleteSource(ctx context.Context, profileID, sourceID ids.UUID, ifVersion *int64) (VoiceCorpusSource, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceCorpusSource{}, err
	}
	var removed VoiceCorpusSource
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		before, err := scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_corpus_source
			WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL`,
			voiceSourceColumns), sourceID, profileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if ifVersion != nil && *ifVersion != before.Version {
			return apperrors.ErrVersionSkew
		}
		now := s.now().UTC()
		removed, err = scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_corpus_source SET
			  content = NULL, content_erased_at = $3, archived_at = $3,
			  excluded = true, exclusion_reason = 'owner_removed',
			  version = version + 1, updated_at = $3
			WHERE id = $1 AND voice_profile_id = $2
			RETURNING %s`, voiceSourceColumns), sourceID, profileID, now))
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "archive", "voice_corpus_source", sourceID,
			map[string]any{"word_count": before.WordCount, "included": !before.Excluded}, nil)
		if err != nil {
			return err
		}
		if err := markProfileStale(ctx, tx, profile, now); err != nil {
			return err
		}
		summary, err := corpusSummary(ctx, tx, profileID)
		if err != nil {
			return err
		}
		hash, err := corpusSourceHash(ctx, tx, profileID)
		if err != nil {
			return err
		}
		wordDelta := 0
		if !before.Excluded {
			wordDelta = -before.WordCount
		}
		return storekit.Emit(ctx, tx, auditID, "voice.corpus_changed", "voice_profile", profileID, map[string]any{
			voiceKeyProfileID: profileID, voiceKeySourceID: sourceID, voiceKeyAction: "removed",
			voiceKeyOrigin: before.Origin, voiceKeyRegister: before.Register, voiceKeyWordDelta: wordDelta,
			voiceKeySourceCount: summary.SourceCount, voiceKeySourceHash: hash,
		})
	})
	return removed, err
}

// ClearCorpus permanently scrubs every retained source/learning body and all
// derived lifecycle rows while preserving the human-authored preferences.
func (s *VoiceStore) ClearCorpus(ctx context.Context, profileID ids.UUID, ifVersion *int64) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfile{}, err
	}
	var cleared VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", profileID, storekit.LiveOnly); err != nil {
			return err
		}
		before, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if ifVersion != nil && *ifVersion != before.Version {
			return apperrors.ErrVersionSkew
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE voice_corpus_source SET content = NULL, content_erased_at = $2,
			  archived_at = coalesce(archived_at, $2), excluded = true,
			  exclusion_reason = coalesce(exclusion_reason, 'corpus_cleared'),
			  version = version + 1, updated_at = $2
			WHERE voice_profile_id = $1`, profileID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE voice_learning_signal SET profile_version = NULL,
			  generated_original = NULL, final_text = NULL, qualifies_as_source = false,
			  content_erased_at = $2, archived_at = coalesce(archived_at, $2),
			  version = version + 1, updated_at = $2
			WHERE voice_profile_id = $1`, profileID, now); err != nil {
			return err
		}
		for _, query := range []string{
			"DELETE FROM voice_profile_delta WHERE voice_profile_id = $1",
			"DELETE FROM voice_build WHERE voice_profile_id = $1",
			"DELETE FROM voice_profile_version WHERE voice_profile_id = $1",
		} {
			if _, err := tx.Exec(ctx, query, profileID); err != nil {
				return err
			}
		}
		cleared, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile SET
			  status = 'collecting', voice_profile_md = '', profile_version = 0,
			  auto_learning_enabled = false, active_source_hash = NULL, last_built_at = NULL,
			  version = version + 1, updated_at = $2
			WHERE id = $1 RETURNING %s`, voiceProfileColumns), profileID, now))
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "erase", "voice_profile", profileID,
			map[string]any{voiceKeyProfileVersion: before.ProfileVersion, voiceKeyStatus: before.Status},
			map[string]any{voiceKeyProfileVersion: 0, voiceKeyStatus: voiceProfileStatusCollecting})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "voice.corpus_changed", "voice_profile", profileID, map[string]any{
			voiceKeyProfileID: profileID, voiceKeyAction: "cleared", voiceKeyWordDelta: 0,
			voiceKeySourceCount: 0, voiceKeySourceHash: "d41d8cd98f00b204e9800998ecf8427e",
		})
	})
	return cleared, err
}
