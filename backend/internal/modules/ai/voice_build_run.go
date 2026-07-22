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
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build
			SET status = 'running', stage = 'snapshot', status_code = NULL, status_detail = NULL,
			    next_attempt_at = NULL, started_at = $3, version = version + 1, updated_at = $3
			WHERE id = $1 AND voice_profile_id = $2 AND archived_at IS NULL
			  AND (status IN ('queued','deferred')
			       OR (status = 'running' AND started_at < $4))
			RETURNING %s`, voiceBuildColumns), buildID, profileID, now, now.Add(-reclaimAfter)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
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
			build, err = s.finishBuildTx(ctx, tx, build, voiceBuildStatusFailed, voiceStatusCodeInternal,
				"the corpus changed after this build was queued; start a new build", now)
			if err != nil {
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
func (s *VoiceStore) DeferBuild(ctx context.Context, buildID ids.UUID, detail string, nextAttempt time.Time) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build
			SET status = 'deferred', status_code = 'budget_deferred', status_detail = $2,
			    next_attempt_at = $3, version = version + 1, updated_at = $4
			WHERE id = $1 AND status = 'running' AND archived_at IS NULL
			RETURNING %s`, voiceBuildColumns), buildID, detail, nextAttempt.UTC(), s.now().UTC()))
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
// detail. The active profile version is untouched by construction.
func (s *VoiceStore) FailBuild(ctx context.Context, buildID ids.UUID, statusCode, safeDetail string) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_build WHERE id = $1 AND status = 'running' AND archived_at IS NULL
			FOR UPDATE`,
			voiceBuildColumns), buildID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := s.finishBuildTx(ctx, tx, build, voiceBuildStatusFailed, statusCode, safeDetail, s.now().UTC()); err != nil {
			return err
		}
		return nil
	})
}

func (s *VoiceStore) finishBuildTx(ctx context.Context, tx pgx.Tx, build VoiceBuild, status, statusCode, detail string, now time.Time) (VoiceBuild, error) {
	finished, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
		UPDATE voice_build
		SET status = $2, status_code = $3, status_detail = $4, completed_at = $5,
		    version = version + 1, updated_at = $5
		WHERE id = $1
		RETURNING %s`, voiceBuildColumns), build.ID, status, nullIfEmpty(statusCode), nullIfEmpty(detail), now))
	if err != nil {
		return VoiceBuild{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_build", finished.ID,
		map[string]any{voiceKeyStatus: build.Status},
		map[string]any{voiceKeyStatus: finished.Status, voiceKeyStatusCode: finished.StatusCode})
	if err != nil {
		return VoiceBuild{}, err
	}
	return finished, emitVoiceBuild(ctx, tx, auditID, finished)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// VoiceBuildOutcome is the evaluated result CompleteBuild persists.
type VoiceBuildOutcome struct {
	Artifact       VoiceArtifact
	Evaluation     map[string]any
	SampleDrafts   []map[string]any
	Guidance       map[string]any
	Classification string
	Action         string // auto_activated | review_required
	StatusCode     string // empty, or quality_regression when the candidate underperformed
	ReviewReasons  []string
	ModelProvider  string
	ModelName      string
}

// CompleteBuild persists one immutable version row with its real evaluation
// and closes the build in the same transaction. auto_activated supersedes
// the active version and promotes the profile; review_required leaves the
// active artifact untouched behind a candidate row.
func (s *VoiceStore) CompleteBuild(ctx context.Context, buildID ids.UUID, outcome VoiceBuildOutcome) (VoiceProfileVersion, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfileVersion{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return VoiceProfileVersion{}, apperrors.ErrPermissionDenied
	}
	var result VoiceProfileVersion
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		build, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_build WHERE id = $1 AND status = 'running' AND archived_at IS NULL
			FOR UPDATE`,
			voiceBuildColumns), buildID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", build.ProfileID, storekit.LiveOnly); err != nil {
			return err
		}
		profile, err := s.visibleProfile(ctx, tx, build.ProfileID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		result, err = s.persistBuildVersion(ctx, tx, build, profile, outcome, actor.ID, now)
		if err != nil {
			return err
		}
		finished, err := scanVoiceBuild(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_build
			SET status = 'succeeded', stage = 'activate', result_version = $2, candidate_action = $3,
			    status_code = $4, status_detail = NULL, completed_at = $5, version = version + 1, updated_at = $5
			WHERE id = $1
			RETURNING %s`, voiceBuildColumns), build.ID, result.ProfileVersion, outcome.Action,
			nullIfEmpty(outcome.StatusCode), now))
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "voice_build", finished.ID,
			map[string]any{voiceKeyStatus: voiceBuildStatusRunning},
			map[string]any{
				voiceKeyStatus: finished.Status, "result_version": result.ProfileVersion,
				voiceKeyCandidateAction: outcome.Action,
			})
		if err != nil {
			return err
		}
		return emitVoiceBuild(ctx, tx, auditID, finished)
	})
	return result, err
}

func (s *VoiceStore) persistBuildVersion(ctx context.Context, tx pgx.Tx, build VoiceBuild, profile VoiceProfile, outcome VoiceBuildOutcome, actorID string, now time.Time) (VoiceProfileVersion, error) {
	var nextVersion int
	var status string
	var activatedAt *time.Time
	if outcome.Action == voiceCandidateAutoActivated {
		var err error
		nextVersion, err = supersedeActiveVoiceVersion(ctx, tx, build.ProfileID, now)
		if err != nil {
			return VoiceProfileVersion{}, err
		}
		status = voiceVersionStatusActive
		activatedAt = &now
	} else {
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(max(profile_version), 0) + 1
			FROM voice_profile_version WHERE voice_profile_id = $1`, build.ProfileID).Scan(&nextVersion); err != nil {
			return VoiceProfileVersion{}, err
		}
		status = voiceVersionStatusCandidate
	}
	profileJSON := map[string]any{
		"inference":     outcome.Artifact.Inference,
		"exemplars":     outcome.Artifact.Exemplars,
		"sample_drafts": outcome.SampleDrafts,
		"guidance":      outcome.Guidance,
	}
	statsJSON, err := json.Marshal(outcome.Artifact.Stats)
	if err != nil {
		return VoiceProfileVersion{}, fmt.Errorf("voice build stats encode: %w", err)
	}
	reviewReasons := outcome.ReviewReasons
	if reviewReasons == nil {
		reviewReasons = []string{}
	}
	version, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
		INSERT INTO voice_profile_version
		  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
		   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
		   model_provider, model_name, builder_version, activation_policy_version,
		   evaluation_json, review_reasons, activated_at, source, captured_by, updated_at)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, '2', $14, $15, $16, 'build', $17, $18)
		RETURNING %s`, voiceVersionColumns),
		build.ProfileID, nextVersion, status, outcome.Artifact.Markdown,
		storekit.JSONArg(profileJSON), statsJSON,
		build.SourceHash, build.SourceCount, build.Reason, voicePredecessor(profile.ProfileVersion),
		outcome.ModelProvider, outcome.ModelName, fmt.Sprintf("voicebuilder/%d", VoiceBuilderVersion),
		storekit.JSONArg(outcome.Evaluation), reviewReasons, activatedAt, actorID, now))
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	if outcome.Action == voiceCandidateAutoActivated {
		if _, err := tx.Exec(ctx, `
			UPDATE voice_profile SET voice_profile_md = $2, profile_version = $3,
			  active_source_hash = $4, status = 'ready', last_built_at = $5,
			  version = version + 1, updated_at = $5
			WHERE id = $1`, build.ProfileID, version.VoiceProfileMD, version.ProfileVersion,
			build.SourceHash, now); err != nil {
			return VoiceProfileVersion{}, err
		}
	}
	if err := insertVoiceBuildDelta(ctx, tx, build, profile, outcome, nextVersion); err != nil {
		return VoiceProfileVersion{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "voice_profile_version", version.ID,
		nil, map[string]any{
			voiceKeyProfileVersion: version.ProfileVersion, voiceKeyStatus: version.Status,
			voiceKeyCandidateAction: outcome.Action,
		})
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	return version, emitVoiceVersion(ctx, tx, auditID, version, outcome.Classification, outcome.Action)
}

// insertVoiceBuildDelta records what this build changed against its
// predecessor — the "what changed" timeline row.
func insertVoiceBuildDelta(ctx context.Context, tx pgx.Tx, build VoiceBuild, profile VoiceProfile, outcome VoiceBuildOutcome, nextVersion int) error {
	delta := map[string]any{
		voiceKeyWordsAdded:   outcome.Artifact.WordCount,
		voiceKeySourcesAdded: build.SourceCount,
	}
	for key, value := range outcome.Evaluation {
		if key == voiceKeyIdentityJaccard || key == voiceKeySignatureJaccard {
			delta[key] = value
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO voice_profile_delta
		  (workspace_id, voice_profile_id, from_version, to_version, classification,
		   activation_outcome, delta_json)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, $4, $5, $6)`,
		build.ProfileID, voicePredecessor(profile.ProfileVersion), nextVersion,
		outcome.Classification, outcome.Action, storekit.JSONArg(delta))
	return err
}

// ActiveVersion returns the profile's current active version, or ok=false
// when no artifact has ever activated.
func (s *VoiceStore) ActiveVersion(ctx context.Context, profileID ids.UUID) (VoiceProfileVersion, bool, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfileVersion{}, false, err
	}
	var version VoiceProfileVersion
	found := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		var err error
		version, err = scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile_version
			WHERE voice_profile_id = $1 AND status = 'active' AND archived_at IS NULL
			ORDER BY profile_version DESC LIMIT 1`, voiceVersionColumns), profileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return version, found, err
}

// VoiceBuildRef locates one due deferred build for the retry sweep.
type VoiceBuildRef struct {
	Workspace   ids.UUID
	ProfileID   ids.UUID
	BuildID     ids.UUID
	RequestedBy *ids.UUID
}

// DueDeferredBuilds walks the fleet for deferred builds whose next attempt
// is due — the capture registry's workspace-by-workspace RLS walk.
func (s *VoiceStore) DueDeferredBuilds(ctx context.Context) ([]VoiceBuildRef, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("voice: listing workspaces for the deferred-build walk: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	var due []VoiceBuildRef
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		err := database.WithWorkspaceTx(wsCtx, s.pool, func(tx pgx.Tx) error {
			wsRows, err := tx.Query(ctx, `
				SELECT id, voice_profile_id, requested_by FROM voice_build
				WHERE status = 'deferred' AND next_attempt_at <= $1 AND archived_at IS NULL`, s.now().UTC())
			if err != nil {
				return err
			}
			defer wsRows.Close()
			for wsRows.Next() {
				ref := VoiceBuildRef{Workspace: wsID}
				if err := wsRows.Scan(&ref.BuildID, &ref.ProfileID, &ref.RequestedBy); err != nil {
					return err
				}
				due = append(due, ref)
			}
			return wsRows.Err()
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("voice: deferred-build walk in workspace %s: %w", wsID, err))
		}
	}
	return due, errs
}
