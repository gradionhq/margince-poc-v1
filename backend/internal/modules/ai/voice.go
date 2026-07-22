// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Voice DNA control-record storage (ADR-0066). Two invariants live here:
// the V1 HTTP surface is the admitted human's one personal profile, and the machine-derived
// voice_profile_md is written ONLY by SetDerivedProfile (which versions
// it) while the human-authored personality_md is written ONLY by
// UpdateProfile — the split that lets a rebuild never destroy the human
// identity; and corpus ingest is idempotent per source_ref, so a
// re-ingested source replaces its row instead of double-counting the
// meter. Every mutation is RBAC-gated on the `voice_profile` object and
// audited and emitted on the owner-private voice stream. Reads repeat the
// owner predicate in SQL: a workspace grant never licenses browsing another
// human's identity or writing corpus.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VoiceStore owns the voice_profile and voice_corpus_source tables
// (tableownership: this module). The profile half lives here; the
// corpus-source half (ingest, manifest, meter) is voicesources.go.
type VoiceStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewVoiceStore(pool *pgxpool.Pool) *VoiceStore {
	return &VoiceStore{pool: pool, now: time.Now}
}

// VoiceProfile is the §B0.2 artifact pair: derived voice_profile_md
// (versioned by ProfileVersion) + human-authored PersonalityMD.
type VoiceProfile struct {
	// note: voice_profile is not in the kernel entity vocabulary (no
	// VoiceProfileKind), so its own id stays untyped; OwnerID names the
	// owning app_user and carries the typed user id.
	ID                  ids.UUID
	OwnerID             *ids.UserID
	Scope               string
	ModelRef            *string
	Status              string
	VoiceProfileMD      string
	ProfileVersion      int
	PersonalityMD       string
	AutoLearningEnabled bool
	ActiveSourceHash    *string
	LastBuiltAt         *time.Time
	Source              string
	CapturedBy          string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           *time.Time
	ArchivedAt          *time.Time
}

// VoiceProfilePage is one keyset page, newest first.
type VoiceProfilePage struct {
	Items      []VoiceProfile
	NextCursor string
	HasMore    bool
}

// CreateVoiceProfileInput: scope defaults to a user profile owned by the
// caller; personality_md may seed the human-authored half.
type CreateVoiceProfileInput struct {
	PersonalityMD string
}

// UpdateVoiceProfileInput carries the two owner-controlled fields. Nil means
// unchanged; the derived artifact remains inaccessible to this write path.
type UpdateVoiceProfileInput struct {
	PersonalityMD       *string
	AutoLearningEnabled *bool
	IfVersion           *int64
}

const voiceProfileColumns = `id, owner_id, scope, model_ref, status, voice_profile_md, profile_version, personality_md, auto_learning_enabled, active_source_hash, last_built_at, source, captured_by, version, created_at, updated_at, archived_at`

func scanVoiceProfile(row pgx.Row) (VoiceProfile, error) {
	var p VoiceProfile
	err := row.Scan(&p.ID, &p.OwnerID, &p.Scope, &p.ModelRef, &p.Status, &p.VoiceProfileMD,
		&p.ProfileVersion, &p.PersonalityMD, &p.AutoLearningEnabled, &p.ActiveSourceHash,
		&p.LastBuiltAt, &p.Source, &p.CapturedBy, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	return p, err
}

// ListProfiles pages the live profiles the caller may see (row-scoped).
func (s *VoiceStore) ListProfiles(ctx context.Context, cursor *string, limit *int) (VoiceProfilePage, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfilePage{}, err
	}
	n := storekit.ClampLimit(limit)
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return VoiceProfilePage{}, apperrors.ErrPermissionDenied
	}
	args := []any{actor.UserID}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := "archived_at IS NULL AND scope = 'user' AND owner_id = $1"
	if cursor != nil && *cursor != "" {
		c, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return VoiceProfilePage{}, err
		}
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID))
	}
	var page VoiceProfilePage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM voice_profile WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`,
			voiceProfileColumns, where, n+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanVoiceProfile(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, p)
		}
		return rows.Err()
	})
	if err != nil {
		return VoiceProfilePage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}

// GetProfile reads one live, visible profile; an archived, foreign, or
// out-of-scope row reads as absent.
func (s *VoiceStore) GetProfile(ctx context.Context, id ids.UUID) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfile{}, err
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = s.visibleProfile(ctx, tx, id)
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

// visibleProfile is the shared row-scoped fetch every profile and source
// path goes through — anything that returns a record is a read and
// carries the scope gate, including the source subpaths.
func (s *VoiceStore) visibleProfile(ctx context.Context, tx pgx.Tx, id ids.UUID) (VoiceProfile, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return VoiceProfile{}, apperrors.ErrPermissionDenied
	}
	p, err := scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM voice_profile
		 WHERE id = $1 AND archived_at IS NULL AND scope = 'user' AND owner_id = $2`,
		voiceProfileColumns), id, actor.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceProfile{}, apperrors.ErrNotFound
	}
	return p, err
}

// ownerOnly guards the personal-content surface of a user-scope
// profile — every content mutation AND the corpus manifest read: the
// corpus is the owner's consented writing, and no row scope — team or
// unbounded — licenses writing in someone else's voice or browsing
// their source list. Team/workspace-scope profiles stay governed by
// grants + row scope alone; profile reads (existence, status, artifact)
// and archive (an admin cleanup act) stay row-scoped.
func ownerOnly(ctx context.Context, p VoiceProfile) error {
	if p.Scope != "user" || p.OwnerID == nil {
		return nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID != p.OwnerID.UUID {
		return fmt.Errorf("a personal voice profile is written only by its owner: %w", apperrors.ErrPermissionDenied)
	}
	return nil
}

// CreateProfile opens the admitted human's one personal profile in the
// collecting state. Team/workspace scopes remain latent storage capability;
// the V1 wire cannot create or browse them.
func (s *VoiceStore) CreateProfile(ctx context.Context, in CreateVoiceProfileInput) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionCreate); err != nil {
		return VoiceProfile{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() || actor.Type != principal.PrincipalHuman {
		return VoiceProfile{}, apperrors.ErrPermissionDenied
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_profile
			  (workspace_id, owner_id, scope, status, personality_md, source, captured_by, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'user', 'collecting', $2, 'ui', $3, $4)
			RETURNING %s`, voiceProfileColumns),
			actor.UserID, in.PersonalityMD, actor.ID, s.now().UTC()))
		if storekit.IsUniqueViolation(err) {
			return fmt.Errorf("%w: a live voice profile already exists for this user — resume it instead of forking a second", apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "voice_profile", p.ID, nil, map[string]any{
			"scope": p.Scope, "status": p.Status,
		})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, p.ID,
			voiceProfileCreatedPayload(p.ID, actor.UserID, Maturity(0), false))
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

// UpdateProfile replaces human-authored preferences and/or the owner's
// automatic-learning opt-in under If-Match. It cannot touch the derived
// artifact or its version.
func (s *VoiceStore) UpdateProfile(ctx context.Context, id ids.UUID, in UpdateVoiceProfileInput) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfile{}, err
	}
	if err := validateVoiceProfileUpdate(in); err != nil {
		return VoiceProfile{}, err
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = s.updateVoiceProfile(ctx, tx, id, in)
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

func validateVoiceProfileUpdate(in UpdateVoiceProfileInput) error {
	if in.PersonalityMD == nil && in.AutoLearningEnabled == nil {
		return &CorpusIngestError{Field: "body", Reason: "provide personality_md or auto_learning_enabled"}
	}
	if in.PersonalityMD != nil && len(*in.PersonalityMD) > 20000 {
		return &CorpusIngestError{Field: voiceKeyPersonalityMD, Reason: "must not exceed 20000 characters"}
	}
	return nil
}

func (s *VoiceStore) updateVoiceProfile(ctx context.Context, tx pgx.Tx, id ids.UUID, in UpdateVoiceProfileInput) (VoiceProfile, error) {
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
	if in.IfVersion != nil && *in.IfVersion != before.Version {
		return VoiceProfile{}, apperrors.ErrVersionSkew
	}
	p, err := scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile SET
			  personality_md = coalesce($2, personality_md),
			  auto_learning_enabled = coalesce($3, auto_learning_enabled),
			  version = version + 1,
			  updated_at = $4
			WHERE id = $1
			RETURNING %s`, voiceProfileColumns),
		id, in.PersonalityMD, in.AutoLearningEnabled, s.now().UTC()))
	if err != nil {
		return VoiceProfile{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_profile", id,
		map[string]any{"personality_md": before.PersonalityMD, "auto_learning_enabled": before.AutoLearningEnabled},
		map[string]any{"personality_md": p.PersonalityMD, "auto_learning_enabled": p.AutoLearningEnabled})
	if err != nil {
		return VoiceProfile{}, err
	}
	summary, err := corpusSummary(ctx, tx, id)
	if err != nil {
		return VoiceProfile{}, err
	}
	if err := emitVoiceProfileUpdates(ctx, tx, auditID, p, in, summary); err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

func emitVoiceProfileUpdates(ctx context.Context, tx pgx.Tx, auditID ids.UUID, profile VoiceProfile, in UpdateVoiceProfileInput, summary CorpusSummary) error {
	if in.PersonalityMD != nil {
		if err := emitVoiceProfileUpdated(ctx, tx, auditID, profile, summary, "preferences_replaced"); err != nil {
			return err
		}
	}
	if in.AutoLearningEnabled == nil {
		return nil
	}
	action := "learning_disabled"
	if *in.AutoLearningEnabled {
		action = "learning_enabled"
	}
	return emitVoiceProfileUpdated(ctx, tx, auditID, profile, summary, action)
}

func emitVoiceProfileUpdated(ctx context.Context, tx pgx.Tx, auditID ids.UUID, profile VoiceProfile, summary CorpusSummary, action string) error {
	return storekit.EmitEvent(ctx, tx, auditID, profile.ID,
		voiceProfileUpdatedPayload(profile.ID, action, profile.Version, Maturity(summary.TotalWords)))
}

// voiceProfileCreatedPayload builds voice.profile_created's typed payload —
// a personal profile always starts collecting, at maturity zero, with
// automatic learning off (CreateProfile never sets otherwise).
func voiceProfileCreatedPayload(profileID, ownerID ids.UUID, maturity string, autoLearningEnabled bool) crmcontracts.WebhookPayloadVoiceProfileCreated {
	return crmcontracts.WebhookPayloadVoiceProfileCreated{
		ProfileId:           openapi_types.UUID(profileID),
		OwnerId:             openapi_types.UUID(ownerID),
		Maturity:            maturity,
		AutoLearningEnabled: autoLearningEnabled,
	}
}

// voiceProfileUpdatedPayload builds voice.profile_updated's typed payload.
func voiceProfileUpdatedPayload(profileID ids.UUID, action string, version int64, maturity string) crmcontracts.WebhookPayloadVoiceProfileUpdated {
	return crmcontracts.WebhookPayloadVoiceProfileUpdated{
		ProfileId: openapi_types.UUID(profileID),
		Action:    action,
		Version:   version,
		Maturity:  maturity,
	}
}

// voiceProfileArchivedPayload builds voice.profile_archived's typed payload.
func voiceProfileArchivedPayload(profileID, ownerID ids.UUID, profileVersion int) crmcontracts.WebhookPayloadVoiceProfileArchived {
	return crmcontracts.WebhookPayloadVoiceProfileArchived{
		ProfileId:      openapi_types.UUID(profileID),
		OwnerId:        openapi_types.UUID(ownerID),
		ProfileVersion: profileVersion,
	}
}

// ArchiveProfile soft-deletes the owner's profile under optimistic
// concurrency and returns the terminal representation for the 200 response.
func (s *VoiceStore) ArchiveProfile(ctx context.Context, id ids.UUID, ifVersion *int64) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionDelete); err != nil {
		return VoiceProfile{}, err
	}
	var archived VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", id, storekit.LiveOnly); err != nil {
			return err
		}
		before, err := s.visibleProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if ifVersion != nil && *ifVersion != before.Version {
			return apperrors.ErrVersionSkew
		}
		archived, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile
			SET archived_at = $2, updated_at = $2, version = version + 1
			WHERE id = $1
			RETURNING %s`, voiceProfileColumns), id, s.now().UTC()))
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "archive", "voice_profile", id,
			map[string]any{"scope": before.Scope, "status": before.Status, "profile_version": before.ProfileVersion}, nil)
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, id,
			voiceProfileArchivedPayload(id, before.OwnerID.UUID, before.ProfileVersion))
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return archived, nil
}
