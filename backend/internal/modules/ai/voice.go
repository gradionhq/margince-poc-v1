// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Voice DNA storage (B-E07.4/.5a, data-model §12.5 extended to the
// features/09 §B0.2 shape). Two invariants live here: the machine-derived
// voice_profile_md is written ONLY by SetDerivedProfile (which versions
// it) while the human-authored personality_md is written ONLY by
// UpdateProfile — the split that lets a rebuild never destroy the human
// identity; and corpus ingest is idempotent per source_ref, so a
// re-ingested source replaces its row instead of double-counting the
// meter. Every mutation is RBAC-gated on the `voice_profile` object and
// audited; the closed catalog (events.md §5) defines no voice.* event
// type, so these writes are ratified audit-only (waived with rationale
// in writeshape_test.go). Reads carry the platform row-scope clause:
// a voice corpus is its owner's personal writing.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	voiceFieldAutoLearning    = "auto_learning_enabled"
	voiceFieldExcluded        = "excluded"
	voiceFieldExclusionReason = "exclusion_reason"
	voiceFieldOutcome         = "outcome"
	voiceFieldProfileID       = "voice_profile_id"
	voiceFieldProfileVersion  = "profile_version"
	voiceFieldReason          = "reason"
	voiceFieldStatus          = "status"
	voiceBuildReasonManual    = "manual"
	voiceBuildStatusRunning   = "running"
	voiceOutcomeDrafted       = "drafted"
	voiceSourceOriginManual   = "manual"
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
	ID               ids.UUID
	OwnerID          *ids.UserID
	Scope            string
	ModelRef         *string
	Status           string
	VoiceProfileMD   string
	ProfileVersion   int
	PersonalityMD    string
	AutoLearning     bool
	ActiveSourceHash string
	LastBuiltAt      *time.Time
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        *time.Time
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
	Scope         string
	PersonalityMD string
}

const voiceProfileColumns = `id, owner_id, scope, model_ref, status, voice_profile_md, profile_version, personality_md, auto_learning_enabled, active_source_hash, last_built_at, version, created_at, updated_at`

func scanVoiceProfile(row pgx.Row) (VoiceProfile, error) {
	var p VoiceProfile
	err := row.Scan(&p.ID, &p.OwnerID, &p.Scope, &p.ModelRef, &p.Status, &p.VoiceProfileMD,
		&p.ProfileVersion, &p.PersonalityMD, &p.AutoLearning, &p.ActiveSourceHash, &p.LastBuiltAt,
		&p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListProfiles pages the live profiles the caller may see (row-scoped).
func (s *VoiceStore) ListProfiles(ctx context.Context, cursor *string, limit *int) (VoiceProfilePage, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return VoiceProfilePage{}, err
	}
	n := storekit.ClampLimit(limit)
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := "archived_at IS NULL"
	scope, err := auth.ScopeClause(ctx, arg)
	if err != nil {
		return VoiceProfilePage{}, err
	}
	if scope != "" {
		where += " AND " + scope
	}
	// Personal voice artifacts are private even when a manager's row scope
	// reaches the owning user. Team/workspace profiles retain normal scope.
	if actor, ok := principal.Actor(ctx); ok && actor.UserID != ids.Nil {
		where += fmt.Sprintf(" AND (scope <> 'user' OR owner_id = $%d)", arg(actor.UserID))
	} else {
		where += " AND scope <> 'user'"
	}
	if cursor != nil && *cursor != "" {
		c, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return VoiceProfilePage{}, err
		}
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID))
	}
	var page VoiceProfilePage
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
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
		if err == nil && ownerOnly(ctx, p) != nil {
			err = apperrors.ErrNotFound
		}
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
	args := []any{id}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := "id = $1 AND archived_at IS NULL"
	scope, err := auth.ScopeClause(ctx, arg)
	if err != nil {
		return VoiceProfile{}, err
	}
	if scope != "" {
		where += " AND " + scope
	}
	p, err := scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM voice_profile WHERE %s`, voiceProfileColumns, where), args...))
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

// CreateProfile opens a voice profile in status=building with an empty
// derived artifact. A user-scope profile is owned by the caller — the
// partial unique index makes a second live one a conflict, so onboarding
// and voice.html always resume THE profile.
func (s *VoiceStore) CreateProfile(ctx context.Context, in CreateVoiceProfileInput) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionCreate); err != nil {
		return VoiceProfile{}, err
	}
	scope := in.Scope
	if scope == "" {
		scope = "user"
	}
	switch scope {
	case "user", "team", "workspace":
	default:
		return VoiceProfile{}, &CorpusIngestError{Field: "scope", Reason: "must be user, team, or workspace"}
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return VoiceProfile{}, apperrors.ErrPermissionDenied
	}
	var owner *ids.UUID
	if scope == "user" {
		owner = storekit.UUIDOrNil(actor.UserID)
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_profile (workspace_id, owner_id, scope, personality_md)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
			RETURNING %s`, voiceProfileColumns),
			owner, scope, in.PersonalityMD))
		if storekit.IsUniqueViolation(err) {
			return fmt.Errorf("%w: a live voice profile already exists for this user — resume it instead of forking a second", apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "voice_profile", p.ID, nil, map[string]any{
			"scope": p.Scope, voiceFieldStatus: p.Status,
		})
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

// UpdateProfile edits the HUMAN-authored personality_md under If-Match.
// It deliberately cannot touch voice_profile_md/profile_version — that
// is SetDerivedProfile's half of the §B0.2 split.
func (s *VoiceStore) UpdateProfile(ctx context.Context, id ids.UUID, personalityMD string, autoLearning *bool, ifVersion *int64) (VoiceProfile, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceProfile{}, err
	}
	var p VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The row lock makes the state read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", id, storekit.LiveOnly); err != nil {
			return err
		}
		before, err := s.visibleProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, before); err != nil {
			return err
		}
		if ifVersion != nil && *ifVersion != before.Version {
			return apperrors.ErrVersionSkew
		}
		p, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile SET personality_md = $2,
			  auto_learning_enabled = coalesce($3, auto_learning_enabled),
			  version = version + 1, updated_at = $4
			WHERE id = $1
			RETURNING %s`, voiceProfileColumns),
			id, personalityMD, autoLearning, s.now().UTC()))
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "voice_profile", id,
			map[string]any{"personality_md": before.PersonalityMD, voiceFieldAutoLearning: before.AutoLearning},
			map[string]any{"personality_md": p.PersonalityMD, voiceFieldAutoLearning: p.AutoLearning})
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

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
		// The row lock makes the state read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "voice_profile", id, storekit.LiveOnly); err != nil {
			return err
		}
		before, err := s.visibleProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, before); err != nil {
			return err
		}
		p, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_profile SET
			  voice_profile_md = $2,
			  profile_version = profile_version + 1,
			  model_ref = coalesce($3, model_ref),
			  status = 'ready',
			  version = version + 1,
			  updated_at = $4
			WHERE id = $1
			RETURNING %s`, voiceProfileColumns),
			id, voiceProfileMD, modelRef, s.now().UTC()))
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "voice_profile", id,
			map[string]any{voiceFieldProfileVersion: before.ProfileVersion, voiceFieldStatus: before.Status},
			map[string]any{voiceFieldProfileVersion: p.ProfileVersion, voiceFieldStatus: p.Status})
		return err
	})
	if err != nil {
		return VoiceProfile{}, err
	}
	return p, nil
}

// ArchiveProfile soft-deletes; the corpus rows stay for the audit trail
// but stop being read (every read path filters on the live parent).
func (s *VoiceStore) ArchiveProfile(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, "voice_profile", principal.ActionDelete); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.visibleProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE voice_profile SET archived_at = $2 WHERE id = $1`, id, s.now().UTC()); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "voice_profile", id,
			map[string]any{"scope": before.Scope, voiceFieldStatus: before.Status, voiceFieldProfileVersion: before.ProfileVersion}, nil)
		return err
	})
}

// ActiveVoiceForUser returns the active artifact for drafting without ever
// exposing corpus content.
func (s *VoiceStore) ActiveVoiceForUser(ctx context.Context, user ids.UUID) (VoiceProfile, error) {
	var profile VoiceProfile
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		profile, err = scanVoiceProfile(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_profile WHERE owner_id = $1 AND scope = 'user' AND archived_at IS NULL`, voiceProfileColumns), user))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	return profile, err
}
