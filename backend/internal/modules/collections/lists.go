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
	ID          ids.UUID
	WorkspaceID ids.UUID
	Name        string
	EntityType  string
	ListType    string
	Definition  map[string]any
	OwnerID     *ids.UUID
	TeamID      *ids.UUID
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
	OwnerID    *ids.UUID
	TeamID     *ids.UUID
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
		_, err = storekit.Audit(ctx, tx, "create", "list", out.ID, nil, map[string]any{
			"name": out.Name, "entity_type": out.EntityType, "list_type": out.ListType,
		})
		return err
	})
	return out, err
}

func (s *Store) GetList(ctx context.Context, id ids.UUID) (listRow, error) {
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

func (s *Store) ArchiveList(ctx context.Context, id ids.UUID) (listRow, error) {
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
		_, err = storekit.Audit(ctx, tx, "archive", "list", id, nil, nil)
		return err
	})
	return out, err
}

type memberRow struct {
	ID         ids.UUID
	ListID     ids.UUID
	EntityType string
	EntityID   ids.UUID
	AddedBy    string
	CreatedAt  time.Time
}

func (s *Store) ListMembers(ctx context.Context, listID ids.UUID, limit int, cursor string) ([]memberRow, storekit.Page, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	var out []memberRow
	var page storekit.Page
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, listID); err != nil {
			return err
		}
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		sql := fmt.Sprintf(`SELECT id, list_id, entity_type, entity_id, added_by, created_at
			FROM list_member WHERE list_id = $%d`, arg(listID))
		if cursor != "" {
			after, err := ids.Parse(cursor)
			if err != nil {
				return &BadInputError{Field: "cursor", Reason: "malformed"}
			}
			sql += fmt.Sprintf(" AND id > $%d", arg(after))
		}
		sql += fmt.Sprintf(" ORDER BY id LIMIT $%d", arg(limit+1))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m memberRow
			if err := rows.Scan(&m.ID, &m.ListID, &m.EntityType, &m.EntityID, &m.AddedBy, &m.CreatedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > limit {
			out = out[:limit]
			page = storekit.Page{HasMore: true, NextCursor: out[limit-1].ID.String()}
		}
		return nil
	})
	return out, page, err
}

func (s *Store) AddMember(ctx context.Context, listID ids.UUID, entityType string, entityID ids.UUID) (memberRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionUpdate); err != nil {
		return memberRow{}, err
	}
	actor, _ := principal.Actor(ctx)
	var out memberRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, listID); err != nil {
			return err
		}
		var listEntityType, listType string
		if err := tx.QueryRow(ctx, `SELECT entity_type, list_type FROM list WHERE id = $1`, listID).
			Scan(&listEntityType, &listType); err != nil {
			return err
		}
		if listType != "static" {
			return &BadInputError{Field: "list", Reason: "a dynamic segment computes its members; only static lists take them"}
		}
		if entityType != listEntityType {
			return &BadInputError{Field: "entity_type", Reason: "must match the list's entity_type " + listEntityType}
		}
		// The member reference is a READ of a row-scoped record (H1).
		if err := auth.EnsureLinkTarget(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO list_member (workspace_id, list_id, entity_type, entity_id, added_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			ON CONFLICT (list_id, entity_type, entity_id) DO NOTHING
			RETURNING id, list_id, entity_type, entity_id, added_by, created_at`,
			listID, entityType, entityID, actor.ID)
		err := rowScanMember(row, &out)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("already a member: %w", apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "list", listID, nil, map[string]any{
			"added": map[string]any{"entity_type": entityType, "entity_id": entityID},
		})
		return err
	})
	return out, err
}

func rowScanMember(row pgx.Row, m *memberRow) error {
	return row.Scan(&m.ID, &m.ListID, &m.EntityType, &m.EntityID, &m.AddedBy, &m.CreatedAt)
}

// ensureListVisible is the list's own row-scope probe (owner_id scoped
// like every other owner-carrying table; ownerless lists are shared).
func ensureListVisible(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	return auth.EnsureVisible(ctx, tx, "list", id)
}

// BadInputError maps to a 422 at the transport.
type BadInputError struct {
	Field  string
	Reason string
}

func (e *BadInputError) Error() string { return "collections: " + e.Field + ": " + e.Reason }

func wireList(l listRow) crmcontracts.List {
	out := crmcontracts.List{
		Id:         openapi_types.UUID(l.ID),
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
		owner := openapi_types.UUID(*l.OwnerID)
		out.OwnerId = &owner
	}
	if l.TeamID != nil {
		team := openapi_types.UUID(*l.TeamID)
		out.TeamId = &team
	}
	return out
}

func wireMember(m memberRow) crmcontracts.ListMember {
	return crmcontracts.ListMember{
		Id:         openapi_types.UUID(m.ID),
		ListId:     openapi_types.UUID(m.ListID),
		EntityType: crmcontracts.ListMemberEntityType(m.EntityType),
		EntityId:   openapi_types.UUID(m.EntityID),
		AddedBy:    &m.AddedBy,
		CreatedAt:  &m.CreatedAt,
	}
}
