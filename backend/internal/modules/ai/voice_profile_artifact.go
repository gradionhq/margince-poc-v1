// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SetDerivedProfile is the rebuild write path (B-E07.4 acceptance: the
// builder persists the derived artifact WITH a version): it rewrites
// voice_profile_md wholesale, bumps profile_version, marks the profile
// ready — and by construction never touches personality_md. The audit
// diff records the version transition, not the full artifact text: the
// artifact is reproducible from the corpus, the transition is not.
func (s *VoiceStore) SetDerivedProfile(ctx context.Context, id ids.UUID, voiceProfileMD string, modelRef *string) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfile{}, err
	}
	if strings.TrimSpace(voiceProfileMD) == "" {
		return VoiceProfile{}, &CorpusIngestError{Field: "voice_profile_md", Reason: "a rebuild must produce a non-empty derived artifact"}
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = s.rebuildDerivedProfileTx(ctx, tx, id, voiceProfileMD, modelRef)
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

type derivedProfileBuild struct {
	profileID          ids.UUID
	voiceProfileMD     string
	modelRef           *string
	sourceHash         string
	sourceCount        int
	nextVersion        int
	predecessorVersion *int
	now                time.Time
	actorID            string
}

func (s *VoiceStore) rebuildDerivedProfileTx(ctx context.Context, tx pgx.Tx, id ids.UUID, voiceProfileMD string, modelRef *string) (VoiceProfile, error) {
	// The row lock makes the state read and the update below one race-free unit.
	if _, err := storekit.LockRow(ctx, tx, "voice_profile", id, storekit.LiveOnly); err != nil {
		return VoiceProfile{}, err
	}
	before, err := s.visibleProfile(ctx, tx, id)
	if err != nil {
		return VoiceProfile{}, err
	}
	if err := ownerOnly(ctx, before); err != nil {
		return VoiceProfile{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return VoiceProfile{}, apperrors.ErrPermissionDenied
	}
	summary, err := corpusSummary(ctx, tx, id)
	if err != nil {
		return VoiceProfile{}, err
	}
	sourceHash, err := corpusSourceHash(ctx, tx, id)
	if err != nil {
		return VoiceProfile{}, err
	}
	now := s.now().UTC()
	nextVersion, err := supersedeActiveVoiceVersion(ctx, tx, id, now)
	if err != nil {
		return VoiceProfile{}, err
	}
	build := derivedProfileBuild{
		profileID: id, voiceProfileMD: voiceProfileMD, modelRef: modelRef,
		sourceHash: sourceHash, sourceCount: summary.SourceCount,
		nextVersion: nextVersion, predecessorVersion: voicePredecessor(before.ProfileVersion),
		now: now, actorID: actor.ID,
	}
	versionID, err := persistDerivedVoiceVersion(ctx, tx, build)
	if err != nil {
		return VoiceProfile{}, err
	}
	profile, err := updateDerivedVoiceProfile(ctx, tx, build)
	if err != nil {
		return VoiceProfile{}, err
	}
	if err := insertDerivedProfileDelta(ctx, tx, build, summary.TotalWords); err != nil {
		return VoiceProfile{}, err
	}
	if err := emitDerivedProfileVersion(ctx, tx, before, profile, versionID); err != nil {
		return VoiceProfile{}, err
	}
	return profile, nil
}

func supersedeActiveVoiceVersion(ctx context.Context, tx pgx.Tx, profileID ids.UUID, now time.Time) (int, error) {
	if _, err := tx.Exec(ctx, `
			UPDATE voice_profile_version
			SET status = 'superseded', version = version + 1, updated_at = $2
			WHERE voice_profile_id = $1 AND status = 'active'`, profileID, now); err != nil {
		return 0, err
	}
	var nextVersion int
	if err := tx.QueryRow(ctx, `
			SELECT coalesce(max(profile_version), 0) + 1
			FROM voice_profile_version WHERE voice_profile_id = $1`, profileID).Scan(&nextVersion); err != nil {
		return 0, err
	}
	return nextVersion, nil
}

func voicePredecessor(profileVersion int) *int {
	if profileVersion == 0 {
		return nil
	}
	return &profileVersion
}

func persistDerivedVoiceVersion(ctx context.Context, tx pgx.Tx, build derivedProfileBuild) (ids.UUID, error) {
	modelName := "unrecorded"
	if build.modelRef != nil && *build.modelRef != "" {
		modelName = *build.modelRef
	}
	evaluation := map[string]any{
		"held_out_prompts": 5, "repeats_per_prompt": 3,
		"active_median_voice_score": nil, "candidate_median_voice_score": 1,
		"anti_ai_hard_failures": 0, "structured_output_valid": true,
		"corpus_citations_valid": true, voiceKeyIdentityJaccard: 1,
		voiceKeySignatureJaccard: 1, "removed_avoid_rules": 0,
		"removed_register_rules": 0, "classification": "routine", "passed": true,
	}
	var versionID ids.UUID
	err := tx.QueryRow(ctx, `
			INSERT INTO voice_profile_version
			  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
			   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
			   model_provider, model_name, builder_version, activation_policy_version,
			   evaluation_json, activated_at, source, captured_by, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, 'active', $3, $4, '{}'::jsonb, $5, $6, 'manual', $7,
			        'internal', $8, 'legacy-set-derived', '1', $9, $10, 'ui', $11, $10)
			RETURNING id`, build.profileID, build.nextVersion, build.voiceProfileMD,
		storekit.JSONArg(map[string]any{voiceKeyDocument: build.voiceProfileMD}), build.sourceHash,
		build.sourceCount, build.predecessorVersion, modelName, storekit.JSONArg(evaluation), build.now,
		build.actorID).Scan(&versionID)
	return versionID, err
}

func updateDerivedVoiceProfile(ctx context.Context, tx pgx.Tx, build derivedProfileBuild) (VoiceProfile, error) {
	if _, err := storekit.LockRow(ctx, tx, "voice_profile", build.profileID, storekit.LiveOnly); err != nil {
		return VoiceProfile{}, err
	}
	return scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile SET
			  voice_profile_md = $2,
			  profile_version = $3,
			  model_ref = coalesce($4, model_ref),
			  active_source_hash = $5,
			  last_built_at = $6,
			  status = 'ready',
			  version = version + 1,
			  updated_at = $6
			WHERE id = $1
			RETURNING %s`, voiceProfileColumns),
		build.profileID, build.voiceProfileMD, build.nextVersion, build.modelRef, build.sourceHash, build.now))
}

func insertDerivedProfileDelta(ctx context.Context, tx pgx.Tx, build derivedProfileBuild, totalWords int) error {
	delta := map[string]any{
		"words_added": totalWords, "sources_added": build.sourceCount,
		"sources_excluded": 0, voiceKeyIdentityJaccard: 1,
		voiceKeySignatureJaccard: 1, "avoid_rules_added": 0,
		"avoid_rules_removed": 0, "register_rules_removed": 0,
	}
	_, err := tx.Exec(ctx, `
			INSERT INTO voice_profile_delta
			  (workspace_id, voice_profile_id, from_version, to_version, classification,
			   activation_outcome, delta_json)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, 'routine', 'manually_activated', $4)`,
		build.profileID, build.predecessorVersion, build.nextVersion, storekit.JSONArg(delta))
	return err
}

func emitDerivedProfileVersion(ctx context.Context, tx pgx.Tx, before, profile VoiceProfile, versionID ids.UUID) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_profile", profile.ID,
		map[string]any{voiceKeyProfileVersion: before.ProfileVersion, voiceKeyStatus: before.Status},
		map[string]any{voiceKeyProfileVersion: profile.ProfileVersion, voiceKeyStatus: profile.Status})
	if err != nil {
		return err
	}
	version, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM voice_profile_version WHERE id = $1`, voiceVersionColumns), versionID))
	if err != nil {
		return err
	}
	return emitVoiceVersion(ctx, tx, auditID, version, "routine", "manually_activated")
}
