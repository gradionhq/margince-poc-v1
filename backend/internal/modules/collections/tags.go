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
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type tagRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Name        string
	Color       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

const tagColumns = `id, workspace_id, name, color, created_at, updated_at, archived_at`

func scanTag(r pgx.Row) (tagRow, error) {
	var t tagRow
	err := r.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Color, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt)
	return t, err
}

// Tags are workspace-shared vocabulary (no owner column): object RBAC
// gates them, row scope does not apply.
func (s *Store) ListTags(ctx context.Context, includeArchived bool) ([]tagRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []tagRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		sql := "SELECT " + tagColumns + " FROM tag"
		if !includeArchived {
			sql += " WHERE archived_at IS NULL"
		}
		rows, err := tx.Query(ctx, sql+" ORDER BY lower(name)")
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
		return rows.Err()
	})
	return out, err
}

func (s *Store) CreateTag(ctx context.Context, name string, color *string) (tagRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionCreate); err != nil {
		return tagRow{}, err
	}
	if strings.TrimSpace(name) == "" {
		return tagRow{}, &BadInputError{Field: "name", Reason: "required"}
	}
	var out tagRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO tag (workspace_id, name, color)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)
			RETURNING `+tagColumns, strings.TrimSpace(name), color)
		var err error
		if out, err = scanTag(row); err != nil {
			if strings.Contains(err.Error(), "uq_tag_name") {
				return fmt.Errorf("tag %q: %w", name, apperrors.ErrConflict)
			}
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "tag", out.ID, nil, map[string]any{"name": out.Name})
		return err
	})
	return out, err
}

func (s *Store) ArchiveTag(ctx context.Context, id ids.UUID) (tagRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionDelete); err != nil {
		return tagRow{}, err
	}
	var out tagRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"UPDATE tag SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING "+tagColumns, id)
		var err error
		if out, err = scanTag(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "tag", id, nil, nil)
		return err
	})
	return out, err
}

type taggableRow struct {
	ID         ids.UUID
	TagID      ids.UUID
	EntityType string
	EntityID   ids.UUID
	CreatedAt  time.Time
}

func (s *Store) ApplyTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) (taggableRow, error) {
	if err := auth.Require(ctx, "tag", principal.ActionUpdate); err != nil {
		return taggableRow{}, err
	}
	if !memberEntityTables[entityType] {
		return taggableRow{}, &BadInputError{Field: "entity_type", Reason: "must be person|organization|deal|lead"}
	}
	var out taggableRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
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
			INSERT INTO taggable (workspace_id, tag_id, entity_type, entity_id)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
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
		_, err = storekit.Audit(ctx, tx, "update", "tag", tagID, nil, map[string]any{
			"applied": map[string]any{"entity_type": entityType, "entity_id": entityID},
		})
		return err
	})
	return out, err
}

func wireTag(t tagRow) crmcontracts.Tag {
	return crmcontracts.Tag{
		Id:          openapi_types.UUID(t.ID),
		WorkspaceId: openapi_types.UUID(t.WorkspaceID),
		Name:        t.Name,
		Color:       t.Color,
		CreatedAt:   &t.CreatedAt,
		UpdatedAt:   &t.UpdatedAt,
		ArchivedAt:  t.ArchivedAt,
	}
}

func wireTaggable(tg taggableRow) crmcontracts.Taggable {
	return crmcontracts.Taggable{
		Id:         openapi_types.UUID(tg.ID),
		TagId:      openapi_types.UUID(tg.TagID),
		EntityType: crmcontracts.TaggableEntityType(tg.EntityType),
		EntityId:   openapi_types.UUID(tg.EntityID),
		CreatedAt:  &tg.CreatedAt,
	}
}
