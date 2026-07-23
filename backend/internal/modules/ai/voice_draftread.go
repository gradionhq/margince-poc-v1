// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The drafting-side voice reads: the actor's own ready profile with its
// active version (what a drafter injects), and the drafted learning signal
// that later feedback (accept / edit / reject) attaches to.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

// voiceLearningSignalRetention bounds how long a drafted signal's plaintext
// survives before the nightly retention evaluator may erase it; long enough
// for the weekly learning delta, far short of forever.
const voiceLearningSignalRetention = 180 * 24 * time.Hour

// ActiveVoiceForActor returns the acting user's ready profile and its
// active version. ok=false — no profile, none ready, or no activated
// artifact — is the drafter's clean-fallback signal, never an error.
func (s *VoiceStore) ActiveVoiceForActor(ctx context.Context) (VoiceProfile, VoiceProfileVersion, bool, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfile{}, VoiceProfileVersion{}, false, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return VoiceProfile{}, VoiceProfileVersion{}, false, apperrors.ErrPermissionDenied
	}
	var (
		profile VoiceProfile
		version VoiceProfileVersion
		found   bool
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile
			WHERE archived_at IS NULL AND scope = 'user' AND owner_id = $1 AND status = 'ready'
			ORDER BY created_at DESC LIMIT 1`, voiceProfileColumns), actor.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := scanVoiceVersion(tx.QueryRow(ctx, storekit.SQLf(`
			SELECT %s FROM voice_profile_version
			WHERE voice_profile_id = $1 AND status = 'active' AND archived_at IS NULL
			ORDER BY profile_version DESC LIMIT 1`, voiceVersionColumns), p.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		profile, version, found = p, v, true
		return nil
	})
	return profile, version, found, err
}

// RecordDraftedSignal remembers that a voice draft was served, keyed by the
// draft reference's hash so later feedback (RejectDraft, the future
// edited-sent capture) lands on this row. A replayed reference is
// idempotent — the first record stands.
func (s *VoiceStore) RecordDraftedSignal(ctx context.Context, profileID ids.UUID, profileVersion int, draftRef, generatedOriginal string) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return apperrors.ErrPermissionDenied
	}
	if draftRef == "" {
		return &CorpusIngestError{Field: voiceKeyDraftRef, Reason: voiceValidationNotEmpty}
	}
	hash := sha256.Sum256([]byte(draftRef))
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.visibleProfile(ctx, tx, profileID); err != nil {
			return err
		}
		now := s.now().UTC()
		var signalID ids.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO voice_learning_signal
			  (workspace_id, voice_profile_id, profile_version, draft_ref_hash, outcome,
			   generated_original, retention_until, source, captured_by, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, 'drafted', $4, $5, 'draft', $6, $7)
			ON CONFLICT (workspace_id, draft_ref_hash) DO NOTHING
			RETURNING id`, profileID, profileVersion, hash[:], generatedOriginal,
			now.Add(voiceLearningSignalRetention), actor.ID, now).Scan(&signalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "voice_learning_signal", signalID, nil,
			map[string]any{
				voiceKeyProfileID: profileID, voiceKeyProfileVersion: profileVersion,
				voiceKeyOutcome: voiceOutcomeDrafted,
			})
		if err != nil {
			return err
		}
		// The drafted signal has not yet been sent, so it qualifies as no
		// learning source and carries no transformations — the accept/edit
		// feedback that later lands on this row is what sets those.
		return storekit.EmitEvent(ctx, tx, auditID, profileID,
			voiceDraftOutcomeRecordedPayload(profileID, voiceOutcomeDrafted))
	})
}

// DecodeVersionStats re-types a stored stats_json back into the builder's
// fingerprint for prompt assembly.
func DecodeVersionStats(version VoiceProfileVersion) VoiceStats {
	stats := VoiceStats{}
	raw, err := json.Marshal(version.StatsJSON)
	if err != nil {
		return stats
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return VoiceStats{}
	}
	return stats
}

// VersionExemplars re-types the stored exemplars for prompt assembly;
// absent or malformed data yields none — the draft simply carries fewer
// examples, never an error.
func VersionExemplars(version VoiceProfileVersion) []VoiceExemplar {
	raw, ok := version.ProfileJSON["exemplars"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var exemplars []VoiceExemplar
	if err := json.Unmarshal(encoded, &exemplars); err != nil {
		return nil
	}
	// The two-example bound is a drafting safety contract, not a storage
	// assumption: more examples teach the model to copy wording.
	if len(exemplars) > 2 {
		exemplars = exemplars[:2]
	}
	return exemplars
}
