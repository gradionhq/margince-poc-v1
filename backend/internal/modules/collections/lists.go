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

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// memberEntityTables is the closed polymorphic target set — the table
// name doubles as the RBAC object and the visibility-probe table.
var memberEntityTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true,
}

const listColumns = `id, workspace_id, name, entity_type, list_type, definition, owner_id, team_id, created_at, updated_at, archived_at`

// catalogCap bounds the un-paginated catalog reads. Lists and tags are
// workspace-curated vocabulary — tens of rows, not record data — which
// is why the contract defines no cursor for them (the missing
// pagination is filed as feedback). The cap keeps a runaway workspace
// from turning the catalog read into an export; truncation is reported
// through the page flag, never silently.
const catalogCap = 1000

type listRow struct {
	ID          ids.ListID
	WorkspaceID ids.WorkspaceID
	Name        string
	EntityType  string
	ListType    string
	Definition  map[string]any
	OwnerID     *ids.UserID
	TeamID      *ids.TeamID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

func scanList(r pgx.Row) (listRow, error) {
	var l listRow
	err := r.Scan(&l.ID, &l.WorkspaceID, &l.Name, &l.EntityType, &l.ListType,
		&l.Definition, &l.OwnerID, &l.TeamID, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt)
	return l, err
}

func (s *Store) ListLists(ctx context.Context, entityType *string, archived storekit.ArchivedFilter) ([]listRow, bool, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return nil, false, err
	}
	var out []listRow
	truncated := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := []string{"true"}
		if entityType != nil {
			where = append(where, fmt.Sprintf("entity_type = $%d", arg(*entityType)))
		}
		if archived != storekit.IncludeArchived {
			where = append(where, "archived_at IS NULL")
		}
		scope, err := auth.ScopeClause(ctx, arg)
		if err != nil {
			return err
		}
		if scope != "" {
			where = append(where, scope)
		}
		rows, err := tx.Query(ctx,
			"SELECT "+listColumns+" FROM list WHERE "+strings.Join(where, " AND ")+
				fmt.Sprintf(" ORDER BY name LIMIT $%d", arg(catalogCap+1)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanList(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
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

type CreateListInput struct {
	Name       string
	EntityType string
	ListType   string
	Definition map[string]any
	OwnerID    *ids.UserID
	TeamID     *ids.TeamID
}

func (s *Store) CreateList(ctx context.Context, in CreateListInput) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionCreate); err != nil {
		return listRow{}, err
	}
	if !memberEntityTables[in.EntityType] {
		return listRow{}, &BadInputError{Field: "entity_type", Reason: "must be person|organization|deal|lead"}
	}
	if in.ListType == "" {
		in.ListType = "static"
	}
	// A dynamic segment IS its definition; a static set must not carry
	// one — the shape rules out a half-and-half list.
	if in.ListType == "dynamic" && len(in.Definition) == 0 {
		return listRow{}, &BadInputError{Field: "definition", Reason: "a dynamic list needs a query definition"}
	}
	if in.ListType == "static" && len(in.Definition) > 0 {
		return listRow{}, &BadInputError{Field: "definition", Reason: "a static list carries no definition"}
	}
	// A dynamic segment's definition is a stored filter the members
	// endpoint later runs through the ONE engine. Validate it against the
	// entity's closed vocabulary NOW so an unknown field or an over-deep
	// tree is rejected at creation (422) rather than at read time — a
	// list cannot store a filter it could never evaluate.
	if in.ListType == "dynamic" {
		if err := validateSegmentDefinition(in.EntityType, in.Definition); err != nil {
			return listRow{}, err
		}
	}
	var out listRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO list (workspace_id, name, entity_type, list_type, definition, owner_id, team_id)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
			RETURNING `+listColumns,
			in.Name, in.EntityType, in.ListType, in.Definition, in.OwnerID, in.TeamID)
		var err error
		if out, err = scanList(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "list", out.ID.UUID, nil, map[string]any{
			"name": out.Name, "entity_type": out.EntityType, "list_type": out.ListType,
		})
		return err
	})
	return out, err
}

func (s *Store) GetList(ctx context.Context, id ids.ListID) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return listRow{}, err
	}
	var out listRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, id); err != nil {
			return err
		}
		var err error
		out, err = scanList(tx.QueryRow(ctx, "SELECT "+listColumns+" FROM list WHERE id = $1", id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return listRow{}, apperrors.ErrNotFound
	}
	return out, err
}

func (s *Store) ArchiveList(ctx context.Context, id ids.ListID) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionDelete); err != nil {
		return listRow{}, err
	}
	var out listRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, id); err != nil {
			return err
		}
		row := tx.QueryRow(ctx,
			"UPDATE list SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING "+listColumns, id)
		var err error
		if out, err = scanList(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound // already archived reads as absent
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "list", id.UUID, nil, nil)
		return err
	})
	return out, err
}

// validateSegmentDefinition proves a dynamic list's definition is an
// evaluable filter over the entity's closed vocabulary before it is
// stored: it compiles the predicate (discarding the SQL) so an unknown
// field, a mistyped value, or an over-deep/over-wide tree fails as a
// PredicateError the transport maps to 422.
func validateSegmentDefinition(entityType string, definition map[string]any) error {
	engine, ok := segmentEngines[entityType]
	if !ok {
		return &BadInputError{Field: "entity_type", Reason: "no dynamic segment engine for " + entityType}
	}
	pred, err := predicateFromDefinition(definition)
	if err != nil {
		return err
	}
	discard := 0
	arg := func(any) int { discard++; return discard }
	_, err = storekit.CompilePredicate(pred, engine.Fields, arg)
	return err
}

// ensureListVisible is the list's own row-scope probe (owner_id scoped
// like every other owner-carrying table; ownerless lists are shared).
func ensureListVisible(ctx context.Context, tx pgx.Tx, id ids.ListID) error {
	return auth.EnsureVisible(ctx, tx, "list", id.UUID)
}

// BadInputError maps to a 422 at the transport.
type BadInputError struct {
	Field  string
	Reason string
}

func (e *BadInputError) Error() string { return "collections: " + e.Field + ": " + e.Reason }

func wireList(l listRow) crmcontracts.List {
	out := crmcontracts.List{
		Id:         openapi_types.UUID(l.ID.UUID),
		Name:       l.Name,
		EntityType: crmcontracts.ListEntityType(l.EntityType),
		ListType:   crmcontracts.ListListType(l.ListType),
		CreatedAt:  &l.CreatedAt,
		UpdatedAt:  &l.UpdatedAt,
		ArchivedAt: l.ArchivedAt,
	}
	if len(l.Definition) > 0 {
		out.Definition = &l.Definition
	}
	if l.OwnerID != nil {
		owner := openapi_types.UUID(l.OwnerID.UUID)
		out.OwnerId = &owner
	}
	if l.TeamID != nil {
		team := openapi_types.UUID(l.TeamID.UUID)
		out.TeamId = &team
	}
	return out
}
