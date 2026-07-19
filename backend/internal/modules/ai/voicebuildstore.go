// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Durable build/version storage. Starting a build and enqueueing its River job
// share one transaction through the callback; model work never holds that
// transaction open.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// VoiceBuild is the durable observable state of one builder run.
type VoiceBuild struct {
	ID              ids.UUID
	ProfileID       ids.UUID
	RequestedBy     ids.UUID
	Reason          string
	Status          string
	Stage           string
	SourceHash      string
	SourceWordCount int
	ResultVersion   *int
	FailureDetail   *string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// VoiceProfileVersion is one immutable compiled voice artifact.
type VoiceProfileVersion struct {
	ID                 ids.UUID
	ProfileID          ids.UUID
	ProfileVersion     int
	VoiceProfileMD     string
	ProfileJSON        json.RawMessage
	StatsJSON          json.RawMessage
	ModelRef           *string
	BuilderVersion     int
	SourceHash         string
	SourceWordCount    int
	Reason             string
	PredecessorVersion *int
	Active             bool
	CreatedAt          time.Time
}

// VoiceProfileDelta explains the evidence change between two versions.
type VoiceProfileDelta struct {
	ID          ids.UUID
	ProfileID   ids.UUID
	FromVersion int
	ToVersion   int
	Summary     json.RawMessage
	CreatedAt   time.Time
}

// VoiceBuildInput is the bounded snapshot claimed by the worker.
type VoiceBuildInput struct {
	Build       VoiceBuild
	Personality string
	Samples     []VoiceSample
}

// VoiceBuildEnqueue inserts the River job in the build-start transaction.
type VoiceBuildEnqueue func(context.Context, pgx.Tx, VoiceBuild) error

const (
	voiceBuildColumns   = `id, voice_profile_id, requested_by, reason, status, stage, source_hash, source_word_count, result_version, failure_detail, created_at, started_at, finished_at`
	voiceVersionColumns = `id, voice_profile_id, profile_version, voice_profile_md, profile_json, stats_json, model_ref, builder_version, source_hash, source_word_count, reason, predecessor_version, active, created_at`
)

func scanVoiceBuild(row pgx.Row) (VoiceBuild, error) {
	var build VoiceBuild
	err := row.Scan(&build.ID, &build.ProfileID, &build.RequestedBy, &build.Reason, &build.Status,
		&build.Stage, &build.SourceHash, &build.SourceWordCount, &build.ResultVersion,
		&build.FailureDetail, &build.CreatedAt, &build.StartedAt, &build.FinishedAt)
	return build, err
}

func scanVoiceVersion(row pgx.Row) (VoiceProfileVersion, error) {
	var version VoiceProfileVersion
	err := row.Scan(&version.ID, &version.ProfileID, &version.ProfileVersion, &version.VoiceProfileMD,
		&version.ProfileJSON, &version.StatsJSON, &version.ModelRef, &version.BuilderVersion,
		&version.SourceHash, &version.SourceWordCount, &version.Reason, &version.PredecessorVersion,
		&version.Active, &version.CreatedAt)
	return version, err
}

// StartBuild snapshots the current corpus and durably queues one idempotent run.
func (s *VoiceStore) StartBuild(ctx context.Context, profileID ids.UUID, reason string, enqueue VoiceBuildEnqueue) (VoiceBuild, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceBuild{}, err
	}
	actor, err := voiceBuildRequester(ctx, reason)
	if err != nil {
		return VoiceBuild{}, err
	}
	var build VoiceBuild
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM voice_profile WHERE id = $1 FOR UPDATE`, profileID).Scan(&profile.ID); err != nil {
			return err
		}
		existing, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_build WHERE voice_profile_id = $1 AND status IN ('queued','running')`, voiceBuildColumns), profileID))
		if err == nil {
			build = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		hash, words, err := voiceSourceSnapshot(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if words < StarterVoiceWords {
			return &CorpusIngestError{Field: "corpus", Reason: fmt.Sprintf("needs at least %d own-authored words; corpus has %d", StarterVoiceWords, words)}
		}
		build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_build (workspace_id, voice_profile_id, requested_by, reason, source_hash, source_word_count)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)
			RETURNING %s`, voiceBuildColumns), profileID, actor.UserID, reason, hash, words))
		if err != nil {
			return err
		}
		if enqueue != nil {
			if err := enqueue(ctx, tx, build); err != nil {
				return err
			}
		}
		if _, err := storekit.Audit(ctx, tx, "create", "voice_build", build.ID, nil, map[string]any{
			voiceFieldProfileID: profileID, voiceFieldReason: reason, "source_hash": hash, "source_word_count": words,
		}); err != nil {
			return err
		}
		if profile.ProfileVersion == 0 {
			_, err = tx.Exec(ctx, `UPDATE voice_profile SET status = 'building', updated_at = $2 WHERE id = $1`, profileID, s.now().UTC())
		}
		return err
	})
	return build, err
}

func voiceBuildRequester(ctx context.Context, reason string) (principal.Principal, error) {
	switch reason {
	case "onboarding", voiceBuildReasonManual, "automatic":
	default:
		return principal.Principal{}, &CorpusIngestError{Field: voiceFieldReason, Reason: "must be onboarding, manual, or automatic"}
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return principal.Principal{}, apperrors.ErrPermissionDenied
	}
	return actor, nil
}

func voiceSourceSnapshot(ctx context.Context, tx pgx.Tx, profileID ids.UUID) (string, int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, content_hash, register, kind, weight, word_count
		FROM voice_corpus_source
		WHERE voice_profile_id = $1 AND NOT excluded
		ORDER BY id`, profileID)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	hash := sha256.New()
	words := 0
	for rows.Next() {
		var id ids.UUID
		var contentHash, register, kind string
		var weight float64
		var count int
		if err := rows.Scan(&id, &contentHash, &register, &kind, &weight, &count); err != nil {
			return "", 0, err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%.1f\n", id, contentHash, register, kind, weight)
		words += count
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), words, nil
}

// GetBuild reads one owner-private durable build.
func (s *VoiceStore) GetBuild(ctx context.Context, profileID, buildID ids.UUID) (VoiceBuild, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceBuild{}, err
	}
	var build VoiceBuild
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		build, err = scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_build WHERE id = $1 AND voice_profile_id = $2`, voiceBuildColumns), buildID, profileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	return build, err
}

// ListVersions returns immutable versions newest first.
func (s *VoiceStore) ListVersions(ctx context.Context, profileID ids.UUID) ([]VoiceProfileVersion, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return nil, err
	}
	var versions []VoiceProfileVersion
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM voice_profile_version WHERE voice_profile_id = $1 ORDER BY profile_version DESC`, voiceVersionColumns), profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			version, err := scanVoiceVersion(rows)
			if err != nil {
				return err
			}
			versions = append(versions, version)
		}
		return rows.Err()
	})
	return versions, err
}

// ClaimBuild moves exactly one queued build to running and returns its bounded
// input snapshot. A retry after a terminal result is an honest no-op.
func (s *VoiceStore) ClaimBuild(ctx context.Context, buildID ids.UUID) (VoiceBuildInput, bool, error) {
	var input VoiceBuildInput
	claimed := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build SET status = 'running', stage = 'analyzing', started_at = $2
			WHERE id = $1 AND status = 'queued'
			RETURNING %s`, voiceBuildColumns), buildID, s.now().UTC()))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		input.Build = build
		if err := tx.QueryRow(ctx, `SELECT personality_md FROM voice_profile WHERE id = $1 AND archived_at IS NULL`, build.ProfileID).Scan(&input.Personality); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, kind, register, weight, content, word_count
			FROM voice_corpus_source
			WHERE voice_profile_id = $1 AND NOT excluded
			ORDER BY created_at, id`, build.ProfileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sample VoiceSample
			if err := rows.Scan(&sample.ID, &sample.Kind, &sample.Register, &sample.Weight, &sample.Text, &sample.WordCount); err != nil {
				return err
			}
			input.Samples = append(input.Samples, sample)
		}
		claimed = true
		return rows.Err()
	})
	return input, claimed, err
}

// CompleteBuild atomically activates a validated artifact and records its delta.
func (s *VoiceStore) CompleteBuild(ctx context.Context, buildID ids.UUID, artifact VoiceArtifact, modelRef string) (VoiceProfileVersion, error) {
	profileJSON, err := json.Marshal(artifact.Profile)
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	statsJSON, err := json.Marshal(artifact.Stats)
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	var version VoiceProfileVersion
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_build WHERE id = $1 FOR UPDATE`, voiceBuildColumns), buildID))
		if err != nil {
			return err
		}
		if build.Status != voiceBuildStatusRunning {
			return apperrors.ErrVersionSkew
		}
		if artifact.SourceHash != build.SourceHash {
			return errors.New("voice build source snapshot changed unexpectedly")
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT profile_version FROM voice_profile WHERE id = $1 FOR UPDATE`, build.ProfileID).Scan(&current); err != nil {
			return err
		}
		next := current + 1
		if _, err := tx.Exec(ctx, `UPDATE voice_profile_version SET active = false WHERE voice_profile_id = $1 AND active`, build.ProfileID); err != nil {
			return err
		}
		version, err = scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_profile_version
			  (workspace_id, voice_profile_id, profile_version, voice_profile_md, profile_json, stats_json,
			   model_ref, builder_version, source_hash, source_word_count, reason, predecessor_version, active)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, 0), true)
			RETURNING %s`, voiceVersionColumns), build.ProfileID, next, artifact.Markdown, profileJSON, statsJSON,
			modelRef, VoiceBuilderVersion, build.SourceHash, build.SourceWordCount, build.Reason, current))
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE voice_profile SET voice_profile_md = $2, profile_version = $3, model_ref = $4,
			  status = 'ready', active_source_hash = $5, last_built_at = $6,
			  version = version + 1, updated_at = $6
			WHERE id = $1`, build.ProfileID, artifact.Markdown, next, modelRef, build.SourceHash, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE voice_build SET status = 'succeeded', stage = 'complete', result_version = $2,
			  finished_at = $3, failure_detail = NULL WHERE id = $1`, build.ID, next, now); err != nil {
			return err
		}
		delta, err := json.Marshal(map[string]any{
			"from_version": current, "to_version": next, "source_word_count": build.SourceWordCount,
			"source_hash": build.SourceHash, voiceFieldReason: build.Reason,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO voice_profile_delta (workspace_id, voice_profile_id, from_version, to_version, summary_json)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)`,
			build.ProfileID, current, next, delta); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "create", "voice_profile_version", version.ID, nil,
			map[string]any{voiceFieldProfileID: build.ProfileID, voiceFieldProfileVersion: next, voiceFieldReason: build.Reason}); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "voice_build", build.ID,
			map[string]any{voiceFieldStatus: voiceBuildStatusRunning}, map[string]any{voiceFieldStatus: "succeeded", "result_version": next})
		return err
	})
	return version, err
}

// FailBuild preserves the last good version and exposes a safe corrective error.
func (s *VoiceStore) FailBuild(ctx context.Context, buildID ids.UUID, failure error) error {
	detail := safeVoiceBuildFailure(failure)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_build WHERE id = $1 FOR UPDATE`, voiceBuildColumns), buildID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || build.Status != voiceBuildStatusRunning {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `UPDATE voice_build SET status = 'failed', stage = 'failed', failure_detail = $2, finished_at = $3 WHERE id = $1`, buildID, detail, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE voice_profile SET status = CASE WHEN profile_version = 0 THEN 'building' ELSE 'stale' END, updated_at = $2 WHERE id = $1`, build.ProfileID, now); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "voice_build", buildID,
			map[string]any{voiceFieldStatus: voiceBuildStatusRunning}, map[string]any{voiceFieldStatus: "failed", "failure_detail": detail})
		return err
	})
}

// RollbackVersion copies an earlier artifact into a new forward version.
func (s *VoiceStore) RollbackVersion(ctx context.Context, profileID ids.UUID, target int) (VoiceProfileVersion, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfileVersion{}, err
	}
	var restored VoiceProfileVersion
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT profile_version FROM voice_profile WHERE id = $1 FOR UPDATE`, profileID).Scan(&profile.ProfileVersion); err != nil {
			return err
		}
		targetVersion, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_profile_version WHERE voice_profile_id = $1 AND profile_version = $2`, voiceVersionColumns), profileID, target))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		next := profile.ProfileVersion + 1
		if _, err := tx.Exec(ctx, `UPDATE voice_profile_version SET active = false WHERE voice_profile_id = $1 AND active`, profileID); err != nil {
			return err
		}
		restored, err = scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_profile_version
			  (workspace_id, voice_profile_id, profile_version, voice_profile_md, profile_json, stats_json,
			   model_ref, builder_version, source_hash, source_word_count, reason, predecessor_version, active)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8, $9, 'rollback', $10, true)
			RETURNING %s`, voiceVersionColumns), profileID, next, targetVersion.VoiceProfileMD,
			targetVersion.ProfileJSON, targetVersion.StatsJSON, targetVersion.ModelRef,
			targetVersion.BuilderVersion, targetVersion.SourceHash, targetVersion.SourceWordCount, profile.ProfileVersion))
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE voice_profile SET voice_profile_md = $2, profile_version = $3, model_ref = $4,
			  status = 'ready', active_source_hash = $5, last_built_at = $6,
			  version = version + 1, updated_at = $6 WHERE id = $1`,
			profileID, restored.VoiceProfileMD, next, restored.ModelRef, restored.SourceHash, now); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "voice_profile_version", restored.ID, nil,
			map[string]any{voiceFieldProfileID: profileID, voiceFieldProfileVersion: next, "rollback_target": target})
		return err
	})
	return restored, err
}

// ListDeltas returns the newest explainable build changes first.
func (s *VoiceStore) ListDeltas(ctx context.Context, profileID ids.UUID) ([]VoiceProfileDelta, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return nil, err
	}
	var deltas []VoiceProfileDelta
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, profile); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, voice_profile_id, from_version, to_version, summary_json, created_at FROM voice_profile_delta WHERE voice_profile_id = $1 ORDER BY created_at DESC`, profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var delta VoiceProfileDelta
			if err := rows.Scan(&delta.ID, &delta.ProfileID, &delta.FromVersion, &delta.ToVersion, &delta.Summary, &delta.CreatedAt); err != nil {
				return err
			}
			deltas = append(deltas, delta)
		}
		return rows.Err()
	})
	return deltas, err
}
