// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
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

type memberRow struct {
	// ID is the list_member row id for a static member, but the record's
	// own id for a computed segment member (which owns no member row) — an
	// overloaded identifier with no single entity kind, so it stays untyped.
	ID     ids.UUID
	ListID ids.ListID
	// EntityType + EntityID are the polymorphic member target (any entity),
	// so the id stays untyped (rule 6).
	EntityType string
	EntityID   ids.UUID
	AddedBy    string
	CreatedAt  time.Time
}

func (s *Store) ListMembers(ctx context.Context, listID ids.ListID, limit int, cursor string) ([]memberRow, storekit.Page, error) {
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
		var listEntityType, listType string
		var definition map[string]any
		if err := tx.QueryRow(ctx, `SELECT entity_type, list_type, definition FROM list WHERE id = $1`, listID).
			Scan(&listEntityType, &listType, &definition); err != nil {
			return err
		}
		// A dynamic segment has no explicit members: its membership IS the
		// live evaluation of its stored filter through the ONE engine. That
		// evaluation composes the caller's row-scope clause itself
		// (Query.SelectIDs), so a team-scoped caller's segment excludes the
		// records they cannot see — the same visibility law the static path
		// enforces with its per-member probe.
		if listType == "dynamic" {
			var segErr error
			out, page, segErr = s.evaluateSegment(ctx, tx, listID, listEntityType, definition, limit, cursor)
			return segErr
		}
		var err error
		out, page, err = s.listStaticMembers(ctx, tx, listID, listEntityType, limit, cursor)
		return err
	})
	return out, page, err
}

// listStaticMembers reads the explicit members of a static list. A list
// holds one entity_type (AddMember enforces it); every member is a row of
// that table. The parent-list gate does not cover the members: without a
// per-member row-scope filter a shared list would leak the existence of
// records outside the caller's scope. So each member is disclosed only if
// its target passes that table's visibility predicate (unbounded actors
// get no filter).
func (s *Store) listStaticMembers(ctx context.Context, tx pgx.Tx, listID ids.ListID, listEntityType string, limit int, cursor string) ([]memberRow, storekit.Page, error) {
	var out []memberRow
	var page storekit.Page
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql := fmt.Sprintf(`SELECT lm.id, lm.list_id, lm.entity_type, lm.entity_id, lm.added_by, lm.created_at
		FROM list_member lm WHERE lm.list_id = $%d`, arg(listID))
	scope, err := auth.ScopeClauseFor(ctx, listEntityType, "e", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		sql += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM %s e WHERE e.id = lm.entity_id AND %s)",
			listEntityType, scope)
	}
	if cursor != "" {
		after, err := ids.Parse(cursor)
		if err != nil {
			return nil, storekit.Page{}, &BadInputError{Field: "cursor", Reason: "malformed"}
		}
		sql += fmt.Sprintf(" AND lm.id > $%d", arg(after))
	}
	sql += fmt.Sprintf(" ORDER BY lm.id LIMIT $%d", arg(limit+1))
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var m memberRow
		if err := rows.Scan(&m.ID, &m.ListID, &m.EntityType, &m.EntityID, &m.AddedBy, &m.CreatedAt); err != nil {
			return nil, storekit.Page{}, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, storekit.Page{}, err
	}
	if len(out) > limit {
		out = out[:limit]
		page = storekit.Page{HasMore: true, NextCursor: out[limit-1].ID.String()}
	}
	return out, page, nil
}

func (s *Store) AddMember(ctx context.Context, listID ids.ListID, entityType string, entityID ids.UUID) (memberRow, error) {
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
		_, err = storekit.Audit(ctx, tx, "update", "list", listID.UUID, nil, map[string]any{
			"added": map[string]any{"entity_type": entityType, "entity_id": entityID},
		})
		return err
	})
	return out, err
}

func rowScanMember(row pgx.Row, m *memberRow) error {
	return row.Scan(&m.ID, &m.ListID, &m.EntityType, &m.EntityID, &m.AddedBy, &m.CreatedAt)
}

// dynamicAddedBy marks a computed segment member: it was never explicitly
// added, so its provenance is the filter itself, not a user.
const dynamicAddedBy = "dynamic"

// evaluateSegment runs a dynamic list's stored filter through the ONE
// engine and returns the matching visible records as members. SelectIDs
// composes the caller's row-scope clause, so the result is already
// existence-hidden to the caller's scope; the ids come back id-ordered,
// which the members endpoint paginates by keyset over the entity id (a
// computed member carries no member-row id of its own, so the record's
// own id IS its stable member identifier).
func (s *Store) evaluateSegment(ctx context.Context, tx pgx.Tx, listID ids.ListID, listEntityType string, definition map[string]any, limit int, cursor string) ([]memberRow, storekit.Page, error) {
	engine, ok := segmentEngines[listEntityType]
	if !ok {
		// A stored list.entity_type outside the segment set is a schema
		// invariant break, not a client error — surface it, never guess.
		return nil, storekit.Page{}, fmt.Errorf("no dynamic segment engine for entity_type %q", listEntityType)
	}
	pred, err := predicateFromDefinition(definition)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	matched, err := engine.SelectIDs(ctx, tx, pred, storekit.PredicateRowLimit)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	var after *ids.UUID
	if cursor != "" {
		parsed, err := ids.Parse(cursor)
		if err != nil {
			return nil, storekit.Page{}, &BadInputError{Field: "cursor", Reason: "malformed"}
		}
		after = &parsed
	}

	out := make([]memberRow, 0, limit)
	var page storekit.Page
	for _, entityID := range matched {
		if after != nil && entityID.String() <= after.String() {
			continue
		}
		if len(out) == limit {
			page = storekit.Page{HasMore: true, NextCursor: out[limit-1].EntityID.String()}
			break
		}
		out = append(out, memberRow{
			ID:         entityID,
			ListID:     listID,
			EntityType: listEntityType,
			EntityID:   entityID,
			AddedBy:    dynamicAddedBy,
		})
	}
	return out, page, nil
}

func wireMember(m memberRow) crmcontracts.ListMember {
	out := crmcontracts.ListMember{
		Id:         openapi_types.UUID(m.ID),
		ListId:     openapi_types.UUID(m.ListID.UUID),
		EntityType: crmcontracts.ListMemberEntityType(m.EntityType),
		EntityId:   openapi_types.UUID(m.EntityID),
		AddedBy:    &m.AddedBy,
	}
	// A computed segment member carries no explicit added-at instant; only
	// a real list_member row does.
	if !m.CreatedAt.IsZero() {
		out.CreatedAt = &m.CreatedAt
	}
	return out
}
