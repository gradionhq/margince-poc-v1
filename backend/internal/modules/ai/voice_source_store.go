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

// IngestSource runs the §B1 pipeline (normalize → speaker-filter →
// register-tag → count) and upserts the manifest row by source_ref:
// re-ingesting a source replaces it — the meter never double-counts —
// and re-adding an excluded source is an explicit opt back in.
func (s *VoiceStore) IngestSource(ctx context.Context, profileID ids.UUID, in IngestSourceInput) (VoiceCorpusSource, CorpusSummary, CorpusIngestStats, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, CorpusIngestStats{}, err
	}
	prepared, err := prepareSource(in)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, CorpusIngestStats{}, err
	}

	var (
		source  VoiceCorpusSource
		summary CorpusSummary
	)
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		source, summary, err = s.ingestPreparedSource(ctx, tx, profileID, prepared)
		return err
	})
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, CorpusIngestStats{}, err
	}
	return source, summary, prepared.Stats, nil
}

// PreviewSource dry-runs one candidate source under the owner gate:
// detected shape and per-speaker word counts, nothing stored.
func (s *VoiceStore) PreviewSource(ctx context.Context, profileID ids.UUID, format, content string) (CorpusPreview, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return CorpusPreview{}, err
	}
	if err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := s.visibleProfile(ctx, tx, profileID)
		return err
	}); err != nil {
		return CorpusPreview{}, err
	}
	return PreviewCorpusText(format, content)
}

type priorVoiceSource struct {
	wordCount int
	excluded  bool
	exists    bool
}

func loadPriorVoiceSource(ctx context.Context, tx pgx.Tx, profileID ids.UUID, sourceRef string) (priorVoiceSource, error) {
	var prior priorVoiceSource
	err := tx.QueryRow(ctx, `
			SELECT word_count, excluded
			FROM voice_corpus_source
			WHERE voice_profile_id = $1 AND source_ref = $2 AND archived_at IS NULL`,
		profileID, sourceRef).Scan(&prior.wordCount, &prior.excluded)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return prior, nil
	case err != nil:
		return prior, err
	default:
		prior.exists = true
		return prior, nil
	}
}

func (s *VoiceStore) ingestPreparedSource(ctx context.Context, tx pgx.Tx, profileID ids.UUID, prepared preparedSource) (VoiceCorpusSource, CorpusSummary, error) {
	profile, err := s.visibleProfile(ctx, tx, profileID)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	if err := ownerOnly(ctx, profile); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return VoiceCorpusSource{}, CorpusSummary{}, apperrors.ErrPermissionDenied
	}
	prior, err := loadPriorVoiceSource(ctx, tx, profileID, prepared.SourceRef)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	source, err := s.persistPreparedSource(ctx, tx, profileID, prepared, actor.ID)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	summary, err := s.recordSourceIngest(ctx, tx, profile, source, prior)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	return source, summary, nil
}

func (s *VoiceStore) persistPreparedSource(ctx context.Context, tx pgx.Tx, profileID ids.UUID, prepared preparedSource, actorID string) (VoiceCorpusSource, error) {
	occurredAt := prepared.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = s.now().UTC()
	}
	var sourceID ids.UUID
	row := tx.QueryRow(ctx, `
			INSERT INTO voice_corpus_source
			  (workspace_id, voice_profile_id, origin, kind, register, weight, source_label,
			   source_ref, content, content_hash, word_count, excluded, exclusion_reason,
			   extractor_version, occurred_at, source, captured_by, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'manual', $2, $3, $4, $5, $6, $7, $8, $9,
			        false, NULL, 'voice-v1', $10, 'ui', $11, $12)
			ON CONFLICT (workspace_id, voice_profile_id, source_ref) DO UPDATE SET
			  origin = 'manual',
			  kind = EXCLUDED.kind,
			  register = EXCLUDED.register,
			  weight = EXCLUDED.weight,
			  source_label = EXCLUDED.source_label,
			  content = EXCLUDED.content,
			  content_hash = EXCLUDED.content_hash,
			  word_count = EXCLUDED.word_count,
			  excluded = false,
			  exclusion_reason = NULL,
			  extractor_version = EXCLUDED.extractor_version,
			  occurred_at = EXCLUDED.occurred_at,
			  content_erased_at = NULL,
			  archived_at = NULL,
			  version = voice_corpus_source.version + 1,
			  updated_at = EXCLUDED.updated_at
			RETURNING id`,
		profileID, prepared.Kind, prepared.Register, prepared.Weight, prepared.Label,
		prepared.SourceRef, prepared.Text, SourceRefForContent(prepared.Text), prepared.Words,
		occurredAt, actorID, s.now().UTC())
	if err := row.Scan(&sourceID); err != nil {
		return VoiceCorpusSource{}, err
	}
	return scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM voice_corpus_source WHERE id = $1`, voiceSourceColumns), sourceID))
}

func (s *VoiceStore) recordSourceIngest(ctx context.Context, tx pgx.Tx, profile VoiceProfile, source VoiceCorpusSource, prior priorVoiceSource) (CorpusSummary, error) {
	auditAction, eventAction, wordDelta := sourceIngestChange(source, prior)
	auditID, err := storekit.Audit(ctx, tx, auditAction, "voice_corpus_source", source.ID, nil, map[string]any{
		"voice_profile_id": profile.ID, voiceKeyKind: source.Kind, voiceKeyRegister: source.Register,
		"source_ref": source.SourceRef, "word_count": source.WordCount,
	})
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
	err = storekit.Emit(ctx, tx, auditID, "voice.corpus_changed", "voice_profile", profile.ID, map[string]any{
		voiceKeyProfileID: profile.ID, voiceKeySourceID: source.ID, voiceKeyAction: eventAction,
		voiceKeyOrigin: source.Origin, voiceKeyRegister: source.Register, voiceKeyWordDelta: wordDelta,
		voiceKeySourceCount: summary.SourceCount, voiceKeySourceHash: sourceHash,
	})
	if err != nil {
		return CorpusSummary{}, err
	}
	return summary, nil
}

func sourceIngestChange(source VoiceCorpusSource, prior priorVoiceSource) (string, string, int) {
	if !prior.exists {
		return "create", "ingested", source.WordCount
	}
	wordDelta := source.WordCount
	if !prior.excluded {
		wordDelta -= prior.wordCount
	}
	return "update", "replaced", wordDelta
}

// ListSources returns the corpus manifest + the live meter for one
// visible profile (features/09 §B1.4 — the onboarding/voice.html read).
func (s *VoiceStore) ListSources(ctx context.Context, profileID ids.UUID) ([]VoiceCorpusSource, CorpusSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return nil, CorpusSummary{}, err
	}
	var (
		sources []VoiceCorpusSource
		summary CorpusSummary
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM voice_corpus_source
			 WHERE voice_profile_id = $1 AND archived_at IS NULL
			 ORDER BY created_at DESC, id DESC`,
			voiceSourceColumns), profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			src, err := scanVoiceSource(rows)
			if err != nil {
				return err
			}
			sources = append(sources, src)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		summary, err = corpusSummary(ctx, tx, profileID)
		return err
	})
	if err != nil {
		return nil, CorpusSummary{}, err
	}
	return sources, summary, nil
}
