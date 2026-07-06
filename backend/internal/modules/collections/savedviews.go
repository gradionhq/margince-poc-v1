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

// A saved view is per-user view state (B-E15.12, runtime-config-surface.md
// §3): it is owned by exactly one human and read back only by that owner.
// V1 is private — shared/team views are a fast-follow — so the store
// stamps and enforces owner_id = the caller and writes shared_scope
// 'private', never widening visibility from the request body. The
// visibility gate here is ownership + tenant RLS, not the shared-record
// row-scope clause, because a view is a personal preference and not a
// workspace record governed by the RBAC object matrix's own/team/all
// scope.

const savedViewColumns = `id, workspace_id, owner_id, shared_scope, resource, name, query, version, created_at, updated_at, archived_at`

// selectSavedView is the shared projection prefix for every saved_view read —
// one spelling so the columns can't drift between the list, get, and delete paths.
const selectSavedView = "SELECT " + savedViewColumns + " FROM saved_view WHERE "

type savedViewRow struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	OwnerID     ids.UUID
	SharedScope string
	Resource    string
	Name        string
	Query       map[string]any
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

func wireSavedView(v savedViewRow) crmcontracts.SavedView {
	scope := crmcontracts.SavedViewSharedScope(v.SharedScope)
	return crmcontracts.SavedView{
		Id:          openapi_types.UUID(v.ID),
		WorkspaceId: openapi_types.UUID(v.WorkspaceID),
		OwnerId:     openapi_types.UUID(v.OwnerID),
		SharedScope: &scope,
		Resource:    crmcontracts.SavedViewResource(v.Resource),
		Name:        v.Name,
		Query:       v.Query,
		Version:     v.Version,
		CreatedAt:   &v.CreatedAt,
		UpdatedAt:   &v.UpdatedAt,
		ArchivedAt:  v.ArchivedAt,
	}
}

func scanSavedView(r pgx.Row) (savedViewRow, error) {
	var v savedViewRow
	err := r.Scan(&v.ID, &v.WorkspaceID, &v.OwnerID, &v.SharedScope, &v.Resource,
		&v.Name, &v.Query, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ArchivedAt)
	return v, err
}

// viewOwner resolves the human whose personal view state a call may touch:
// the acting user, or — for an agent/passport call — the human it acts on
// behalf of ("agent ≤ human"). A principal with no human identity (the
// system actor) cannot own a personal view.
func viewOwner(ctx context.Context) (ids.UUID, error) {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	switch {
	case !p.UserID.IsZero():
		return p.UserID, nil
	case !p.OnBehalfOf.IsZero():
		return p.OnBehalfOf, nil
	default:
		return ids.Nil, fmt.Errorf("a saved view needs a human owner: %w", apperrors.ErrPermissionDenied)
	}
}

func (s *Store) ListSavedViews(ctx context.Context, resource *string, archived storekit.ArchivedFilter) ([]savedViewRow, bool, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionRead); err != nil {
		return nil, false, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return nil, false, err
	}
	var out []savedViewRow
	truncated := false
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := []string{fmt.Sprintf("owner_id = $%d", arg(owner))}
		if resource != nil {
			where = append(where, fmt.Sprintf("resource = $%d", arg(*resource)))
		}
		if archived != storekit.IncludeArchived {
			where = append(where, "archived_at IS NULL")
		}
		rows, err := tx.Query(ctx,
			selectSavedView+strings.Join(where, " AND ")+
				fmt.Sprintf(" ORDER BY name, id LIMIT $%d", arg(catalogCap+1)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanSavedView(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
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

type CreateSavedViewInput struct {
	Resource string
	Name     string
	Query    map[string]any
}

func (s *Store) CreateSavedView(ctx context.Context, in CreateSavedViewInput) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionCreate); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return savedViewRow{}, &BadInputError{Field: "name", Reason: "must not be empty"}
	}
	if in.Query == nil {
		return savedViewRow{}, &BadInputError{Field: "query", Reason: "must not be null"}
	}
	var out savedViewRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO saved_view (workspace_id, owner_id, shared_scope, resource, name, query)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'private', $2, $3, $4)
			RETURNING `+savedViewColumns,
			owner, in.Resource, in.Name, in.Query)
		var err error
		if out, err = scanSavedView(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "saved_view", out.ID, nil, map[string]any{
			"resource": out.Resource, "name": out.Name,
		})
		return err
	})
	return out, err
}

func (s *Store) GetSavedView(ctx context.Context, id ids.UUID) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionRead); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSavedView(tx.QueryRow(ctx,
			selectSavedView+"id = $1 AND owner_id = $2", id, owner))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another user's view (or none) reads as absent — existence-hiding.
		return savedViewRow{}, apperrors.ErrNotFound
	}
	return out, err
}

type UpdateSavedViewInput struct {
	Name      *string
	Query     *map[string]any
	IfVersion *int64
}

func (s *Store) UpdateSavedView(ctx context.Context, id ids.UUID, in UpdateSavedViewInput) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionUpdate); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanSavedView(tx.QueryRow(ctx,
			selectSavedView+"id = $1 AND owner_id = $2 AND archived_at IS NULL", id, owner))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.Name != nil {
			p.Set("name", current.Name, *in.Name)
		}
		if in.Query != nil {
			p.Set("query", current.Query, *in.Query)
		}
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.Apply(ctx, tx, "saved_view", id, in.IfVersion); err != nil {
			return fmt.Errorf("apply saved_view patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "saved_view", id, p.Before(), p.After()); err != nil {
			return err
		}
		out, err = scanSavedView(tx.QueryRow(ctx,
			selectSavedView+"id = $1", id))
		return err
	})
	return out, err
}

func (s *Store) ArchiveSavedView(ctx context.Context, id ids.UUID) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionDelete); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"UPDATE saved_view SET archived_at = now() WHERE id = $1 AND owner_id = $2 AND archived_at IS NULL RETURNING "+savedViewColumns,
			id, owner)
		var err error
		if out, err = scanSavedView(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound // absent, others', or already archived — all read as absent
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "saved_view", id, nil, nil)
		return err
	})
	return out, err
}
