// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The completing half of the build state machine: the one transaction that
// turns a validated artifact plus its real evaluation into an immutable
// version row, the active-version read the evaluator compares against, and
// the fleet sweep that re-offers deferred or stranded builds.

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
// active artifact untouched behind a candidate row. claimedAt fences the
// write to this claim generation, and a corpus edited mid-run demotes the
// result to review — an artifact from an obsolete snapshot never silently
// replaces the active voice.
func (s *VoiceStore) CompleteBuild(ctx context.Context, buildID ids.UUID, claimedAt time.Time, outcome VoiceBuildOutcome) (VoiceProfileVersion, error) {
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
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", build.ProfileID, storekit.LiveOnly); err != nil {
			return err
		}
		profile, err := s.visibleProfile(ctx, tx, build.ProfileID)
		if err != nil {
			return err
		}
		currentHash, err := corpusSourceHash(ctx, tx, build.ProfileID)
		if err != nil {
			return err
		}
		if currentHash != build.SourceHash && outcome.Action == voiceCandidateAutoActivated {
			outcome.Action = voiceCandidateReviewRequired
			outcome.ReviewReasons = append(outcome.ReviewReasons,
				"the corpus changed while this build was running; review before activating")
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

// staleQueuedAge is how long a queued build may sit before the sweep
// re-offers it: the in-transaction enqueue makes a lost job rare (a crash
// between commit and job pickup, or a row predating the runner), and the
// per-build job uniqueness makes a duplicate offer harmless.
const staleQueuedAge = 10 * time.Minute

// DueDeferredBuilds walks the fleet for builds the sweep should re-offer:
// deferred rows whose next attempt is due, and queued rows old enough that
// their job evidently never ran — the capture registry's
// workspace-by-workspace RLS walk.
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
			now := s.now().UTC()
			wsRows, err := tx.Query(ctx, `
				SELECT id, voice_profile_id, requested_by FROM voice_build
				WHERE archived_at IS NULL
				  AND ((status = 'deferred' AND next_attempt_at <= $1)
				       OR (status = 'queued' AND created_at <= $2))`, now, now.Add(-staleQueuedAge))
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
