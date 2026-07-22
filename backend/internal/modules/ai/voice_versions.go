// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Derived Voice DNA versions are immutable candidates. Activation never
// destroys the last known-good artifact, and rollback moves forward by
// copying an earlier artifact into a new version.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VoiceProfileVersion is an immutable generated Voice DNA artifact and its review state.
type VoiceProfileVersion struct {
	ID                      ids.UUID
	ProfileID               ids.UUID
	ProfileVersion          int
	Status                  string
	VoiceProfileMD          string
	ProfileJSON             map[string]any
	StatsJSON               map[string]any
	SourceHash              string
	SourceCount             int
	Reason                  string
	PredecessorVersion      *int
	ModelProvider           string
	ModelName               string
	BuilderVersion          string
	ActivationPolicyVersion string
	Evaluation              map[string]any
	ReviewReasons           []string
	Source                  string
	CapturedBy              string
	Version                 int64
	CreatedAt               time.Time
	UpdatedAt               *time.Time
	ArchivedAt              *time.Time
	ActivatedAt             *time.Time
}

// VoiceProfileVersionPage is one keyset-paginated slice of immutable versions.
type VoiceProfileVersionPage struct {
	Items      []VoiceProfileVersion
	NextCursor string
	HasMore    bool
}

const voiceVersionColumns = `id, voice_profile_id, profile_version, status, voice_profile_md, profile_json, stats_json, source_hash, source_count, reason, predecessor_version, model_provider, model_name, builder_version, activation_policy_version, evaluation_json, review_reasons, source, captured_by, version, created_at, updated_at, archived_at, activated_at`

func scanVoiceVersion(row pgx.Row) (VoiceProfileVersion, error) {
	var (
		version                          VoiceProfileVersion
		profileJSON, statsJSON, evalJSON []byte
	)
	err := row.Scan(&version.ID, &version.ProfileID, &version.ProfileVersion, &version.Status,
		&version.VoiceProfileMD, &profileJSON, &statsJSON, &version.SourceHash,
		&version.SourceCount, &version.Reason, &version.PredecessorVersion, &version.ModelProvider,
		&version.ModelName, &version.BuilderVersion, &version.ActivationPolicyVersion, &evalJSON,
		&version.ReviewReasons, &version.Source, &version.CapturedBy, &version.Version,
		&version.CreatedAt, &version.UpdatedAt, &version.ArchivedAt, &version.ActivatedAt)
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	if err := json.Unmarshal(profileJSON, &version.ProfileJSON); err != nil {
		return VoiceProfileVersion{}, fmt.Errorf("decode stored voice profile document: %w", err)
	}
	if err := json.Unmarshal(statsJSON, &version.StatsJSON); err != nil {
		return VoiceProfileVersion{}, fmt.Errorf("decode stored voice stats: %w", err)
	}
	if err := json.Unmarshal(evalJSON, &version.Evaluation); err != nil {
		return VoiceProfileVersion{}, fmt.Errorf("decode stored voice evaluation: %w", err)
	}
	return version, nil
}

// ListVersions returns owner-visible versions in newest-first order.
func (s *VoiceStore) ListVersions(ctx context.Context, profileID ids.UUID, cursor *string, limit *int) (VoiceProfileVersionPage, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfileVersionPage{}, err
	}
	n := storekit.ClampLimit(limit)
	args := []any{profileID}
	where := "voice_profile_id = $1 AND archived_at IS NULL"
	if cursor != nil && *cursor != "" {
		decoded, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return VoiceProfileVersionPage{}, err
		}
		args = append(args, decoded.CreatedAt, decoded.ID)
		where += " AND (created_at, id) < ($2, $3)"
	}
	var page VoiceProfileVersionPage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile_version
			WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`, voiceVersionColumns, where, n+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanVoiceVersion(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return VoiceProfileVersionPage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}

// ApplyVersion makes a candidate version active under optimistic concurrency.
func (s *VoiceStore) ApplyVersion(ctx context.Context, profileID ids.UUID, profileVersion int, ifVersion *int64) (VoiceProfileVersion, error) {
	return s.transitionVersion(ctx, profileID, profileVersion, ifVersion, "active")
}

// RejectVersion closes a candidate without changing the active profile.
func (s *VoiceStore) RejectVersion(ctx context.Context, profileID ids.UUID, profileVersion int, ifVersion *int64) (VoiceProfileVersion, error) {
	return s.transitionVersion(ctx, profileID, profileVersion, ifVersion, "rejected")
}

func (s *VoiceStore) transitionVersion(ctx context.Context, profileID ids.UUID, profileVersion int, ifVersion *int64, target string) (VoiceProfileVersion, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfileVersion{}, err
	}
	var result VoiceProfileVersion
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		result, err = s.transitionVoiceVersion(ctx, tx, profileID, profileVersion, ifVersion, target)
		return err
	})
	return result, err
}

func (s *VoiceStore) transitionVoiceVersion(ctx context.Context, tx pgx.Tx, profileID ids.UUID, profileVersion int, ifVersion *int64, target string) (VoiceProfileVersion, error) {
	if _, err := storekit.LockRow(ctx, tx, "voice_profile", profileID, storekit.LiveOnly); err != nil {
		return VoiceProfileVersion{}, err
	}
	if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
		return VoiceProfileVersion{}, err
	}
	candidate, err := loadCandidateVoiceVersion(ctx, tx, profileID, profileVersion)
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	if ifVersion != nil && *ifVersion != candidate.Version {
		return VoiceProfileVersion{}, apperrors.ErrVersionSkew
	}
	now := s.now().UTC()
	if target == voiceVersionStatusActive {
		if err := activateCandidateVoiceVersion(ctx, tx, candidate, now); err != nil {
			return VoiceProfileVersion{}, err
		}
	}
	result, err := setVoiceVersionStatus(ctx, tx, candidate, target, now)
	if err != nil {
		return VoiceProfileVersion{}, err
	}
	if err := recordVoiceVersionTransition(ctx, tx, result, target); err != nil {
		return VoiceProfileVersion{}, err
	}
	return result, nil
}

func loadCandidateVoiceVersion(ctx context.Context, tx pgx.Tx, profileID ids.UUID, profileVersion int) (VoiceProfileVersion, error) {
	candidate, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile_version
			WHERE voice_profile_id = $1 AND profile_version = $2 AND status = 'candidate'
			  AND archived_at IS NULL`, voiceVersionColumns), profileID, profileVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceProfileVersion{}, apperrors.ErrNotFound
	}
	return candidate, err
}

func activateCandidateVoiceVersion(ctx context.Context, tx pgx.Tx, candidate VoiceProfileVersion, now time.Time) error {
	if _, err := storekit.LockRow(ctx, tx, "voice_profile", candidate.ProfileID, storekit.LiveOnly); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
				UPDATE voice_profile_version
				SET status = 'superseded', version = version + 1, updated_at = $2
				WHERE voice_profile_id = $1 AND status = 'active'`, candidate.ProfileID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
				UPDATE voice_profile SET voice_profile_md = $2, profile_version = $3,
				  active_source_hash = $4, status = 'ready', last_built_at = $5,
				  version = version + 1, updated_at = $5
				WHERE id = $1`, candidate.ProfileID, candidate.VoiceProfileMD, candidate.ProfileVersion,
		candidate.SourceHash, now)
	return err
}

func setVoiceVersionStatus(ctx context.Context, tx pgx.Tx, candidate VoiceProfileVersion, target string, now time.Time) (VoiceProfileVersion, error) {
	var activatedAt *time.Time
	if target == voiceVersionStatusActive {
		activatedAt = &now
	}
	return scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile_version
			SET status = $3, activated_at = $4, version = version + 1, updated_at = $5
			WHERE voice_profile_id = $1 AND profile_version = $2
			RETURNING %s`, voiceVersionColumns), candidate.ProfileID, candidate.ProfileVersion, target, activatedAt, now))
}

func recordVoiceVersionTransition(ctx context.Context, tx pgx.Tx, result VoiceProfileVersion, target string) error {
	outcome := voiceOutcomeRejected
	auditAction := "reject"
	if target == voiceVersionStatusActive {
		outcome = "manually_activated"
		auditAction = "update"
	}
	var classification string
	if err := tx.QueryRow(ctx, `
			UPDATE voice_profile_delta SET activation_outcome = $3
			WHERE voice_profile_id = $1 AND to_version = $2
			RETURNING classification`, result.ProfileID, result.ProfileVersion, outcome).Scan(&classification); err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, auditAction, "voice_profile_version", result.ID,
		map[string]any{voiceKeyStatus: voiceVersionStatusCandidate}, map[string]any{voiceKeyStatus: target})
	if err != nil {
		return err
	}
	return emitVoiceVersion(ctx, tx, auditID, result, classification, outcome)
}

// RollbackVersion copies a prior active artifact into a new immutable active version.
func (s *VoiceStore) RollbackVersion(ctx context.Context, profileID ids.UUID, sourceVersion int) (VoiceProfileVersion, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfileVersion{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return VoiceProfileVersion{}, apperrors.ErrPermissionDenied
	}
	var result VoiceProfileVersion
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", profileID, storekit.LiveOnly); err != nil {
			return err
		}
		profile, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		source, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile_version
			WHERE voice_profile_id = $1 AND profile_version = $2
			  AND status IN ('active','superseded') AND archived_at IS NULL`, voiceVersionColumns), profileID, sourceVersion))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		now := s.now().UTC()
		nextVersion, err := supersedeActiveVoiceVersion(ctx, tx, profileID, now)
		if err != nil {
			return err
		}
		result, err = scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_profile_version
			  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
			   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
			   model_provider, model_name, builder_version, activation_policy_version,
			   evaluation_json, review_reasons, activated_at, source, captured_by, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, 'active', $3, $4, $5, $6, $7, 'rollback', $8,
			        $9, $10, $11, $12, $13, $14, $15, 'ui', $16, $15)
			RETURNING %s`, voiceVersionColumns), profileID, nextVersion, source.VoiceProfileMD,
			storekit.JSONArg(source.ProfileJSON), storekit.JSONArg(source.StatsJSON), source.SourceHash,
			source.SourceCount, profile.ProfileVersion, source.ModelProvider, source.ModelName,
			source.BuilderVersion, source.ActivationPolicyVersion, storekit.JSONArg(source.Evaluation),
			source.ReviewReasons, now, actor.ID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE voice_profile SET voice_profile_md = $2, profile_version = $3,
			  active_source_hash = $4, status = 'ready', last_built_at = $5,
			  version = version + 1, updated_at = $5
			WHERE id = $1`, profileID, result.VoiceProfileMD, result.ProfileVersion,
			result.SourceHash, now); err != nil {
			return err
		}
		delta := map[string]any{
			"words_added": 0, "sources_added": 0, "sources_excluded": 0,
			voiceKeyIdentityJaccard: 1, voiceKeySignatureJaccard: 1,
			"avoid_rules_added": 0, "avoid_rules_removed": 0, "register_rules_removed": 0,
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO voice_profile_delta
			  (workspace_id, voice_profile_id, from_version, to_version, classification,
			   activation_outcome, delta_json)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, 'routine', 'rollback', $4)`,
			profileID, profile.ProfileVersion, result.ProfileVersion, storekit.JSONArg(delta)); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "restore", "voice_profile_version", result.ID, nil,
			map[string]any{voiceKeyProfileVersion: result.ProfileVersion, "predecessor_version": profile.ProfileVersion})
		if err != nil {
			return err
		}
		return emitVoiceVersion(ctx, tx, auditID, result, "routine", "rollback")
	})
	return result, err
}

func emitVoiceVersion(ctx context.Context, tx pgx.Tx, auditID ids.UUID, version VoiceProfileVersion, classification, outcome string) error {
	return storekit.EmitEvent(ctx, tx, auditID, version.ProfileID,
		voiceVersionChangedPayload(version, classification, outcome))
}

// voiceVersionChangedPayload builds voice.version_changed's typed payload.
// PredecessorVersion is nil for a profile's first version, which has none.
func voiceVersionChangedPayload(version VoiceProfileVersion, classification, outcome string) crmcontracts.WebhookPayloadVoiceVersionChanged {
	return crmcontracts.WebhookPayloadVoiceVersionChanged{
		ProfileId:          openapi_types.UUID(version.ProfileID),
		ProfileVersion:     version.ProfileVersion,
		Status:             version.Status,
		Reason:             version.Reason,
		PredecessorVersion: version.PredecessorVersion,
		Classification:     classification,
		ActivationOutcome:  outcome,
	}
}
