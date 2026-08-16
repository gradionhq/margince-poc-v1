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

const savedViewColumns = `id, owner_id, shared_scope, resource, name, query, version, created_at, updated_at, archived_at`

// The one key inside a saved view's `query` that carries a filter tree, and the
// wire field a refusal of it names. Spelled once so the create/update gate and
// the export path cannot look under different keys.
const (
	viewFilterKey  = "filter"
	viewQueryField = "query"
)

// selectSavedView is the shared projection prefix for every saved_view read —
// one spelling so the columns can't drift between the list, get, and delete paths.
const selectSavedView = "SELECT " + savedViewColumns + " FROM saved_view WHERE "

type savedViewRow struct {
	ID          ids.SavedViewID
	OwnerID     ids.UserID
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
		Id:          openapi_types.UUID(v.ID.UUID),
		OwnerId:     openapi_types.UUID(v.OwnerID.UUID),
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
	err := r.Scan(&v.ID, &v.OwnerID, &v.SharedScope, &v.Resource,
		&v.Name, &v.Query, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ArchivedAt)
	return v, err
}

// viewOwner resolves the human whose personal view state a call may touch:
// the acting user, or — for an agent/passport call — the human it acts on
// behalf of ("agent ≤ human"). A principal with no human identity (the
// system actor) cannot own a personal view.
func viewOwner(ctx context.Context) (ids.UserID, error) {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return ids.UserID{}, err
	}
	// The principal carries the human identity as an untyped platform-seam
	// UUID; a saved view's owner is a user, so it is asserted as such here.
	switch {
	case !p.UserID.IsZero():
		return ids.From[ids.UserKind](p.UserID), nil
	case !p.OnBehalfOf.IsZero():
		return ids.From[ids.UserKind](p.OnBehalfOf), nil
	default:
		return ids.UserID{}, fmt.Errorf("a saved view needs a human owner: %w", apperrors.ErrPermissionDenied)
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
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
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
	if err := s.validateViewFilter(ctx, in.Resource, in.Query); err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO saved_view (owner_id, shared_scope, resource, name, query)
			VALUES ($1, 'private', $2, $3, $4)
			RETURNING `+savedViewColumns,
			owner, in.Resource, in.Name, in.Query)
		var err error
		if out, err = scanSavedView(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "saved_view", out.ID.UUID, nil, map[string]any{
			"resource": out.Resource, "name": out.Name,
		})
		return err
	})
	return out, err
}

func (s *Store) GetSavedView(ctx context.Context, id ids.SavedViewID) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionRead); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
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

// validateViewFilter proves a saved view's filter state is evaluable against
// its resource's closed vocabulary before the view is stored — the same proof
// CreateList runs over a dynamic list's definition, and for the same reason:
// otherwise the first thing that ever checks the filter is an export, so the
// author learns their view is unusable from a surface they reached much later
// rather than from the one that accepted it.
//
// Three states are legitimately unvalidatable and pass through:
//
//   - A view with NO filter state. A saved view is columns, sort and grouping
//     as much as it is a filter.
//   - A filter of `null`, which is how a client spells "cleared" — the same
//     answer as absent, and read the same way at export.
//   - A resource with no segment engine (activities, partners are not
//     predicate-leaf resources). There is no vocabulary to check against; the
//     export path refuses such a view outright on its own grounds.
func (s *Store) validateViewFilter(ctx context.Context, resource string, query map[string]any) error {
	rawFilter, present := query[viewFilterKey]
	if !present || rawFilter == nil {
		return nil
	}
	engineKey, filterable := viewResourceToEngine[resource]
	if !filterable {
		return nil
	}
	filterMap, isTree := rawFilter.(map[string]any)
	if !isTree {
		return &BadInputError{Field: viewQueryField, Reason: "filter state must be a filter tree"}
	}
	engine, ok, err := s.SegmentEngine(ctx, engineKey)
	if err != nil {
		return err
	}
	if !ok {
		// viewResourceToEngine named an engine key that segmentEngines does not
		// carry — the two maps have drifted, which is ours to fix rather than
		// the caller's to work around.
		return fmt.Errorf("saved view resource %q maps to %q, which has no segment engine", resource, engineKey)
	}
	return compileForValidation(engine, filterMap, viewQueryField)
}

type UpdateSavedViewInput struct {
	Name      *string
	Query     *map[string]any
	IfVersion *int64
}

func (s *Store) UpdateSavedView(ctx context.Context, id ids.SavedViewID, in UpdateSavedViewInput) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionUpdate); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	// A replaced filter is checked too, or the create-time gate is a formality
	// one PATCH walks around. Resolved BEFORE the write transaction opens:
	// SegmentEngine reads the custom-field catalogue on its own pool
	// connection, and a second acquire while holding a transaction is the
	// deadlock shape the sibling seams spell out.
	if in.Query != nil {
		current, err := s.GetSavedView(ctx, id)
		if err != nil {
			return savedViewRow{}, err
		}
		// The write below answers not-found for an archived view. Refusing its
		// filter first would tell the caller it exists.
		if current.ArchivedAt != nil {
			return savedViewRow{}, apperrors.ErrNotFound
		}
		// The stored resource, never a caller-supplied one: a view's resource is
		// immutable (this input carries no Resource), so the vocabulary a filter
		// is checked against is the one it will actually be evaluated with.
		if err := s.validateViewFilter(ctx, current.Resource, *in.Query); err != nil {
			return savedViewRow{}, err
		}
	}
	var out savedViewRow
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
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
		if err := p.ApplyGuarded(ctx, tx, "saved_view", id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply saved_view patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "saved_view", id.UUID, p.Before(), p.After()); err != nil {
			return err
		}
		out, err = scanSavedView(tx.QueryRow(ctx,
			selectSavedView+"id = $1", id))
		return err
	})
	return out, err
}

func (s *Store) ArchiveSavedView(ctx context.Context, id ids.SavedViewID) (savedViewRow, error) {
	if err := auth.Require(ctx, "saved_view", principal.ActionDelete); err != nil {
		return savedViewRow{}, err
	}
	owner, err := viewOwner(ctx)
	if err != nil {
		return savedViewRow{}, err
	}
	var out savedViewRow
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"UPDATE saved_view SET archived_at = now() WHERE id = $1 AND owner_id = $2 AND archived_at IS NULL RETURNING "+savedViewColumns,
			id, owner)
		var err error
		if out, err = scanSavedView(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound // absent, others', or already archived — all read as absent
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "saved_view", id.UUID, nil, nil)
		return err
	})
	return out, err
}
