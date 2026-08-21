// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type tagRow struct {
	ID         ids.TagID
	Name       string
	Color      *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

const tagColumns = `id, name, color, created_at, updated_at, archived_at`

func scanTag(r pgx.Row) (tagRow, error) {
	var t tagRow
	err := r.Scan(&t.ID, &t.Name, &t.Color, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt)
	return t, err
}

// Tags are workspace-shared vocabulary (no owner column): object RBAC
// gates them, row scope does not apply. The read is bounded by the same
// catalogCap as lists — see the constant for why the contract has no
// cursor here.
func (s *Store) ListTags(ctx context.Context, archived storekit.ArchivedFilter) ([]tagRow, bool, error) {
	if err := auth.Require(ctx, "tag", principal.ActionRead); err != nil {
		return nil, false, err
	}
	var out []tagRow
	truncated := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		sql := "SELECT " + tagColumns + " FROM tag"
		if archived != storekit.IncludeArchived {
			sql += " WHERE archived_at IS NULL"
		}
		rows, err := tx.Query(ctx, sql+" ORDER BY lower(name) LIMIT $1", catalogCap+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTag(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > catalogCap {
			out = out[:catalogCap]
			truncated = true
		}
		return nil
	})
	return out, truncated, err
}

func (s *Store) CreateTag(ctx context.Context, name string, color *string) (tagRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionCreate); err != nil {
		return tagRow{}, err
	}
	if strings.TrimSpace(name) == "" {
		return tagRow{}, &BadInputError{Field: "name", Reason: "required"}
	}
	var out tagRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO tag (name, color)
			VALUES ($1, $2)
			RETURNING `+tagColumns, strings.TrimSpace(name), color)
		var err error
		if out, err = scanTag(row); err != nil {
			if constraint, ok := storekit.UniqueViolation(err); ok && constraint == "uq_tag_name" {
				return fmt.Errorf("tag %q: %w", name, apperrors.ErrConflict)
			}
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "tag", out.ID.UUID, nil, map[string]any{"name": out.Name})
		return err
	})
	return out, err
}

func (s *Store) ArchiveTag(ctx context.Context, id ids.TagID) (tagRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionDelete); err != nil {
		return tagRow{}, err
	}
	var out tagRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"UPDATE tag SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING "+tagColumns, id)
		var err error
		if out, err = scanTag(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "tag", id.UUID, nil, nil)
		return err
	})
	return out, err
}

type taggableRow struct {
	// ID is the taggable link-row id — a join row, not a first-class
	// entity, so it stays untyped.
	ID    ids.UUID
	TagID ids.TagID
	// EntityType + EntityID are the polymorphic tag target (any entity),
	// so the id stays untyped (rule 6).
	EntityType string
	EntityID   ids.UUID
	CreatedAt  time.Time
}

func (s *Store) ApplyTag(ctx context.Context, tagID ids.TagID, entityType string, entityID ids.UUID) (taggableRow, error) {
	// Required by the contract, true only if checked: the zero UUID would reach
	// the row-scope link gate and answer not-found for a record nobody named.
	if err := httperr.RequireBodyID(entityIDField, entityID); err != nil {
		return taggableRow{}, err
	}
	if err := auth.Require(ctx, "tag", principal.ActionUpdate); err != nil {
		return taggableRow{}, err
	}
	if !memberEntityTables[entityType] {
		return taggableRow{}, &BadInputError{Field: entityTypeField, Reason: "must be " + memberEntityVocabulary}
	}
	var out taggableRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var archived *time.Time
		err := tx.QueryRow(ctx, `SELECT archived_at FROM tag WHERE id = $1`, tagID).Scan(&archived)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && archived != nil) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		// Tagging a record is a READ of it (H1): the reference is
		// client-supplied and row-scoped.
		if err := auth.EnsureLinkTarget(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO taggable (tag_id, entity_type, entity_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (tag_id, entity_type, entity_id) DO NOTHING
			RETURNING id, tag_id, entity_type, entity_id, created_at`,
			tagID, entityType, entityID)
		err = row.Scan(&out.ID, &out.TagID, &out.EntityType, &out.EntityID, &out.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("already tagged: %w", apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "tag", tagID.UUID, nil, map[string]any{
			"applied": map[string]any{"entity_type": entityType, "entity_id": entityID},
		})
		return err
	})
	return out, err
}

// RemoveTag takes ONE tag off ONE record, leaving the tag itself alone.
//
// The vocabulary had no way back. ApplyTag added a tagging and ArchiveTag
// retired a tag from the whole workspace, so the only way to undo a mistaken
// tag was to retire the tag for everybody — which is not undo, it is a second
// mistake with a wider blast radius.
//
// Same gates as applying, for the same reasons: `tag` update authority, and
// EnsureLinkTarget because naming a record you cannot see must answer
// not-found rather than confirming it exists by refusing differently.
// Removing a tagging that is not there is NOT an error — the caller asked for
// a state, and the state is already true (idempotent by intent, which is what
// makes a retry safe).
func (s *Store) RemoveTag(ctx context.Context, tagID ids.TagID, entityType string, entityID ids.UUID) error {
	if err := httperr.RequireBodyID(entityIDField, entityID); err != nil {
		return err
	}
	if err := auth.Require(ctx, "tag", principal.ActionUpdate); err != nil {
		return err
	}
	if !memberEntityTables[entityType] {
		return &BadInputError{Field: entityTypeField, Reason: "must be " + memberEntityVocabulary}
	}
	// READ on the target's own object type, not only tag.update. Without it a
	// role holding tag.update and deal.read=false could take taggings off
	// deals it may not see, and learn from the refusal whether a given deal id
	// exists at all.
	if err := auth.Require(ctx, entityType, principal.ActionRead); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var archived *time.Time
		err := tx.QueryRow(ctx, `SELECT archived_at FROM tag WHERE id = $1`, tagID).Scan(&archived)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && archived != nil) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := auth.EnsureLinkTarget(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM taggable WHERE tag_id = $1 AND entity_type = $2 AND entity_id = $3`,
			tagID, entityType, entityID)
		if err != nil {
			return err
		}
		// Audited only when something was actually removed: an audit row for a
		// tagging that was never there describes an event that did not happen.
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = storekit.Audit(ctx, tx, "update", "tag", tagID.UUID, nil, map[string]any{
			"removed": map[string]any{"entity_type": entityType, "entity_id": entityID},
		})
		return err
	})
}

// EnsureTaggable refuses a record this caller may not tag.
//
// Split out so a caller can ask BEFORE it creates anything: apply-by-name used
// to mint the tag first and check the record second, leaving a live audited
// word behind when the record turned out not to exist or to sit outside the
// caller's row scope.
//
// It also requires READ on the target's object type, which EnsureLinkTarget
// alone does not: a role holding tag.update but not deal.read could otherwise
// take taggings off deals it may not see, and tell an existing deal from a
// missing one by which refusal came back.
func (s *Store) EnsureTaggable(ctx context.Context, entityType string, entityID ids.UUID) error {
	if !memberEntityTables[entityType] {
		return &BadInputError{Field: entityTypeField, Reason: "must be " + memberEntityVocabulary}
	}
	if err := auth.Require(ctx, entityType, principal.ActionRead); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		return auth.EnsureLinkTarget(ctx, tx, entityType, entityID)
	})
}

// FindTag answers the id of the LIVE tag with this name, or ok=false.
//
// Case-insensitive, matching the uq_tag_name index, and live-only: an archived
// word was retired on purpose and is not what a caller naming it means.
func (s *Store) FindTag(ctx context.Context, name string) (ids.UUID, bool, error) {
	if err := auth.Require(ctx, "tag", principal.ActionRead); err != nil {
		return ids.UUID{}, false, err
	}
	var id ids.TagID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id FROM tag
			 WHERE lower(name) = lower($1) AND archived_at IS NULL`, strings.TrimSpace(name)).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, false, nil
	}
	if err != nil {
		return ids.UUID{}, false, fmt.Errorf("collections: finding tag by name: %w", err)
	}
	return id.UUID, true, nil
}

// TagSummary is the tag as another module sees it. tagRow is unexported
// because its shape is this store's business; the seam that carries a tag
// across a module boundary carries this instead.
type TagSummary struct {
	TagID    ids.UUID
	Name     string
	Color    string
	Archived bool
}

func tagSummary(t tagRow) TagSummary {
	out := TagSummary{TagID: t.ID.UUID, Name: t.Name, Archived: t.ArchivedAt != nil}
	if t.Color != nil {
		out.Color = *t.Color
	}
	return out
}

// TagVocabulary lists the workspace's tags for a caller outside this module.
func (s *Store) TagVocabulary(ctx context.Context, includeArchived bool) ([]TagSummary, error) {
	filter := storekit.LiveOnly
	if includeArchived {
		filter = storekit.IncludeArchived
	}
	rows, _, err := s.ListTags(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]TagSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, tagSummary(r))
	}
	return out, nil
}

// NewTag creates one and answers it in the cross-module shape.
func (s *Store) NewTag(ctx context.Context, name, color string) (TagSummary, error) {
	var colorArg *string
	if color != "" {
		colorArg = &color
	}
	row, err := s.CreateTag(ctx, name, colorArg)
	if err != nil {
		return TagSummary{}, err
	}
	return tagSummary(row), nil
}

func wireTag(t tagRow) crmcontracts.Tag {
	return crmcontracts.Tag{
		Id:         openapi_types.UUID(t.ID.UUID),
		Name:       t.Name,
		Color:      t.Color,
		CreatedAt:  &t.CreatedAt,
		UpdatedAt:  &t.UpdatedAt,
		ArchivedAt: t.ArchivedAt,
	}
}

func wireTaggable(tg taggableRow) crmcontracts.Taggable {
	return crmcontracts.Taggable{
		Id:         openapi_types.UUID(tg.ID),
		TagId:      openapi_types.UUID(tg.TagID.UUID),
		EntityType: crmcontracts.TaggableEntityType(tg.EntityType),
		EntityId:   openapi_types.UUID(tg.EntityID),
		CreatedAt:  &tg.CreatedAt,
	}
}
