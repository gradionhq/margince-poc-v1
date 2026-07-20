// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Durable Voice DNA builds survive budget deferral and snapshot the corpus
// that the eventual builder will use.

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

// VoiceBuild is the durable, corpus-snapshot request for producing a profile version.
type VoiceBuild struct {
	ID              ids.UUID
	ProfileID       ids.UUID
	Reason          string
	Status          string
	Stage           *string
	SourceHash      string
	SourceCount     int
	ResultVersion   *int
	CandidateAction string
	StatusCode      *string
	StatusDetail    *string
	NextAttemptAt   *time.Time
	Version         int64
	CreatedAt       time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	UpdatedAt       *time.Time
	ArchivedAt      *time.Time
}

// CreateVoiceBuildInput identifies the human-approved reason for a build.
type CreateVoiceBuildInput struct {
	Reason string
}

const voiceBuildColumns = `id, voice_profile_id, reason, status, stage, source_hash, source_count, result_version, candidate_action, status_code, status_detail, next_attempt_at, version, created_at, started_at, completed_at, updated_at, archived_at`

func scanVoiceBuild(row pgx.Row) (VoiceBuild, error) {
	var build VoiceBuild
	err := row.Scan(&build.ID, &build.ProfileID, &build.Reason, &build.Status, &build.Stage,
		&build.SourceHash, &build.SourceCount, &build.ResultVersion, &build.CandidateAction,
		&build.StatusCode, &build.StatusDetail, &build.NextAttemptAt, &build.Version,
		&build.CreatedAt, &build.StartedAt, &build.CompletedAt, &build.UpdatedAt, &build.ArchivedAt)
	return build, err
}

// CreateBuild returns an already-active build for retry safety; otherwise it
// snapshots the current included corpus into one durable queued request.
func (s *VoiceStore) CreateBuild(ctx context.Context, profileID ids.UUID, in CreateVoiceBuildInput) (VoiceBuild, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceBuild{}, err
	}
	if in.Reason != voiceBuildReasonOnboarding && in.Reason != voiceBuildReasonManual {
		return VoiceBuild{}, &CorpusIngestError{Field: voiceKeyReason, Reason: "must be onboarding or manual"}
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return VoiceBuild{}, apperrors.ErrPermissionDenied
	}
	var build VoiceBuild
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", profileID, storekit.LiveOnly); err != nil {
			return err
		}
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		var err error
		build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_build
			WHERE voice_profile_id = $1 AND status IN ('queued','deferred','running')
			  AND archived_at IS NULL
			ORDER BY created_at DESC, id DESC LIMIT 1`, voiceBuildColumns), profileID))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var totalWords, sourceCount int
		var sourceHash string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(word_count), 0)::int, count(*)::int,
			       md5(coalesce(string_agg(content_hash, ',' ORDER BY source_ref), ''))
			FROM voice_corpus_source
			WHERE voice_profile_id = $1 AND NOT excluded AND archived_at IS NULL
			  AND content_erased_at IS NULL`, profileID).Scan(&totalWords, &sourceCount, &sourceHash); err != nil {
			return err
		}
		if totalWords < 800 {
			return &CorpusIngestError{Field: "corpus", Reason: "at least 800 eligible own-authored words are required"}
		}
		build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_build
			  (workspace_id, voice_profile_id, requested_by, reason, status, source_hash,
			   source_count, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, 'queued', $4, $5, $6)
			ON CONFLICT DO NOTHING
			RETURNING %s`, voiceBuildColumns), profileID, actor.UserID, in.Reason,
			sourceHash, sourceCount, s.now().UTC()))
		if errors.Is(err, pgx.ErrNoRows) {
			build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
				SELECT %s FROM voice_build
				WHERE voice_profile_id = $1 AND status IN ('queued','deferred','running')
				  AND archived_at IS NULL
				ORDER BY created_at DESC, id DESC LIMIT 1`, voiceBuildColumns), profileID))
			return err
		}
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "voice_build", build.ID, nil, map[string]any{
			"voice_profile_id": profileID, voiceKeyReason: build.Reason, voiceKeyStatus: build.Status,
			voiceKeySourceHash: build.SourceHash, voiceKeySourceCount: build.SourceCount,
		})
		if err != nil {
			return err
		}
		return emitVoiceBuild(ctx, tx, auditID, build)
	})
	return build, err
}

// GetBuild returns an owner-visible build belonging to the requested profile.
func (s *VoiceStore) GetBuild(ctx context.Context, profileID, buildID ids.UUID) (VoiceBuild, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceBuild{}, err
	}
	var build VoiceBuild
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		var err error
		build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_build
			WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL`, voiceBuildColumns), buildID, profileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	return build, err
}

func emitVoiceBuild(ctx context.Context, tx pgx.Tx, auditID ids.UUID, build VoiceBuild) error {
	return storekit.Emit(ctx, tx, auditID, "voice.build_changed", "voice_profile", build.ProfileID, map[string]any{
		voiceKeyProfileID: build.ProfileID, "build_id": build.ID, voiceKeyReason: build.Reason,
		voiceKeyStatus: build.Status, "stage": build.Stage, voiceKeySourceHash: build.SourceHash,
		voiceKeySourceCount: build.SourceCount, "result_version": build.ResultVersion,
		"candidate_action": build.CandidateAction, "status_code": build.StatusCode,
		"next_attempt_at": build.NextAttemptAt,
	})
}
