// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The build runner's durable state machine over voice_build: claim (with
// crash-safe reclaim), stage transitions, budget deferral, terminal failure,
// and the one transaction that turns a validated artifact plus its real
// evaluation into an immutable version row. The model calls themselves live
// in compose; this file owns every voice_build row transition.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	voiceBuildStatusSucceeded = "succeeded"
	voiceBuildStatusFailed    = "failed"

	voiceCandidateAutoActivated  = "auto_activated"
	voiceCandidateReviewRequired = "review_required"

	voiceStatusCodeBudgetDeferred   = "budget_deferred"
	voiceStatusCodeModelUnavailable = "model_unavailable"
	voiceStatusCodeInvalidOutput    = "invalid_output"
	voiceStatusCodeRegression       = "quality_regression"
	voiceStatusCodeInternal         = "internal"
)

// VoiceBuildEnqueue hands a freshly created build to the job runner INSIDE
// the creating transaction, so a committed build row always has its job and
// a rolled-back one never does. Nil means no runner is configured: the row
// stays queued and GetBuild reports that honestly.
type VoiceBuildEnqueue func(ctx context.Context, tx pgx.Tx, build VoiceBuild) error

// WithBuildEnqueue returns a store whose CreateBuild also queues the job.
func (s *VoiceStore) WithBuildEnqueue(enqueue VoiceBuildEnqueue) *VoiceStore {
	copied := *s
	copied.enqueueBuild = enqueue
	return &copied
}

// WithVoiceBuildEnqueue rebinds the handler set's voice store with the
// in-transaction job enqueue hook.
func (h Handlers) WithVoiceBuildEnqueue(enqueue VoiceBuildEnqueue) Handlers {
	h.voice = h.voice.WithBuildEnqueue(enqueue)
	return h
}

// VoiceBuildInput is everything one build run needs, loaded under the claim
// transaction so the run works from one consistent corpus snapshot.
type VoiceBuildInput struct {
	Build       VoiceBuild
	Profile     VoiceProfile
	Personality string
	Samples     []VoiceSample
}

// ClaimBuild moves a queued or due-deferred build to running and loads its
// input. A running row older than reclaimAfter is reclaimed (its worker
// died); claimed=false reports a terminal or actively-worked row — the
// caller stops without error, the row already owns its state. When the
// corpus changed between queue and claim, the build fails honestly instead
// of building from a snapshot nobody asked for.
func (s *VoiceStore) ClaimBuild(ctx context.Context, profileID, buildID ids.UUID, reclaimAfter time.Duration) (VoiceBuildInput, bool, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceBuildInput{}, false, err
	}
	var input VoiceBuildInput
	claimed := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		// A deferred row is claimable only once its budget window reopens —
		// a duplicate or early job delivery must not defeat the backoff.
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build
			SET status = 'running', stage = 'snapshot', status_code = NULL, status_detail = NULL,
			    next_attempt_at = NULL, started_at = $3, version = version + 1, updated_at = $3
			WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL
			  AND (status = 'queued'
			       OR (status = 'deferred' AND next_attempt_at <= $3)
			       OR (status = 'running' AND started_at < $4))
			RETURNING %s`, voiceBuildColumns), buildID, profileID, now, now.Add(-reclaimAfter)))
		if errors.Is(err, pgx.ErrNoRows) {
			// Not claimable right now. Report the row's state so the caller
			// can distinguish "terminal, stop" from "still owned or waiting,
			// come back" — silently succeeding here would strand the build.
			unclaimed, stateErr := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
				SELECT %s FROM voice_build
				WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL`,
				voiceBuildColumns), buildID, profileID))
			if errors.Is(stateErr, pgx.ErrNoRows) {
				return nil
			}
			if stateErr != nil {
				return stateErr
			}
			input.Build = unclaimed
			return nil
		}
		if err != nil {
			return err
		}
		// The profile row lock serializes this snapshot against corpus
		// mutations: the hash check and the sample read below must see one
		// consistent corpus, not two READ COMMITTED points in time.
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", profileID, storekit.LiveOnly); err != nil {
			return err
		}
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		currentHash, err := corpusSourceHash(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if currentHash != build.SourceHash {
			if err := s.finishBuildTx(ctx, tx, build, voiceBuildStatusFailed, voiceStatusCodeInternal,
				"the corpus changed after this build was queued; start a new build", now); err != nil {
				return err
			}
			return nil
		}
		samples, err := loadVoiceSamples(ctx, tx, profileID)
		if err != nil {
			return err
		}
		input = VoiceBuildInput{Build: build, Profile: profile, Personality: profile.PersonalityMD, Samples: samples}
		claimed = true
		return nil
	})
	return input, claimed, err
}

// loadVoiceSamples reads the included, un-erased corpus rows with their
// content — the exact set the snapshot hash covers.
func loadVoiceSamples(ctx context.Context, tx pgx.Tx, profileID ids.UUID) ([]VoiceSample, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, kind, register, weight, content, word_count
		FROM voice_corpus_source
		WHERE voice_profile_id = $1 AND NOT excluded AND archived_at IS NULL
		  AND content_erased_at IS NULL
		ORDER BY occurred_at, id`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var samples []VoiceSample
	for rows.Next() {
		var sample VoiceSample
		var id ids.UUID
		if err := rows.Scan(&id, &sample.Kind, &sample.Register, &sample.Weight, &sample.Text, &sample.WordCount); err != nil {
			return nil, err
		}
		sample.ID = id.String()
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

// SetBuildStage records forward progress; the stage is display state, so a
// lost update is harmless and the write is deliberately minimal.
func (s *VoiceStore) SetBuildStage(ctx context.Context, buildID ids.UUID, stage string) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE voice_build SET stage = $2, version = version + 1, updated_at = $3
			WHERE id = $1 AND status = 'running' AND archived_at IS NULL`, buildID, stage, s.now().UTC())
		return err
	})
}

// DeferBuild parks a running build until the next budget window — the only
// deferral the schema admits; an unavailable model fails closed instead.
// claimedAt fences the write to THIS claim generation: a worker whose row
// was reclaimed can no longer win a terminal transition.
func (s *VoiceStore) DeferBuild(ctx context.Context, buildID ids.UUID, claimedAt time.Time, detail string, nextAttempt time.Time) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build
			SET status = 'deferred', status_code = 'budget_deferred', status_detail = $2,
			    next_attempt_at = $3, version = version + 1, updated_at = $4
			WHERE id = $1 AND status = 'running' AND started_at = $5 AND archived_at IS NULL
			RETURNING %s`, voiceBuildColumns), buildID, detail, nextAttempt.UTC(), s.now().UTC(), claimedAt.UTC()))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "voice_build", build.ID,
			map[string]any{voiceKeyStatus: voiceBuildStatusRunning},
			map[string]any{voiceKeyStatus: build.Status, voiceKeyStatusCode: build.StatusCode, "next_attempt_at": build.NextAttemptAt})
		if err != nil {
			return err
		}
		return emitVoiceBuild(ctx, tx, auditID, build)
	})
}

// FailBuild records a terminal failure with an actionable, internals-free
// detail. The active profile version is untouched by construction; claimedAt
// fences the write to this claim generation.
func (s *VoiceStore) FailBuild(ctx context.Context, buildID ids.UUID, claimedAt time.Time, statusCode, safeDetail string) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_build
			WHERE id = $1 AND status = 'running' AND started_at = $2 AND archived_at IS NULL
			FOR UPDATE`,
			voiceBuildColumns), buildID, claimedAt.UTC()))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.finishBuildTx(ctx, tx, build, voiceBuildStatusFailed, statusCode, safeDetail, s.now().UTC()); err != nil {
			return err
		}
		return nil
	})
}

func (s *VoiceStore) finishBuildTx(ctx context.Context, tx pgx.Tx, build VoiceBuild, status, statusCode, detail string, now time.Time) error {
	finished, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
		UPDATE voice_build
		SET status = $2, status_code = $3, status_detail = $4, completed_at = $5,
		    version = version + 1, updated_at = $5
		WHERE id = $1
		RETURNING %s`, voiceBuildColumns), build.ID, status, nullIfEmpty(statusCode), nullIfEmpty(detail), now))
	if err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_build", finished.ID,
		map[string]any{voiceKeyStatus: build.Status},
		map[string]any{voiceKeyStatus: finished.Status, voiceKeyStatusCode: finished.StatusCode})
	if err != nil {
		return err
	}
	return emitVoiceBuild(ctx, tx, auditID, finished)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
