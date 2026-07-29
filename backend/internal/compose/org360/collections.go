// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The two membership sections: the tags applied to the account, and the
// lists it belongs to. Tags are workspace-shared; lists carry an owner, so
// only the ones the caller can read are named.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// tagsSection reads the tags applied to the account.
func tagsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]crmcontracts.Tag, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.workspace_id, t.name, t.color, t.created_at, t.updated_at, t.archived_at
		FROM tag t
		JOIN taggable g ON g.tag_id = t.id AND g.entity_type = 'organization' AND g.entity_id = $1
		WHERE t.archived_at IS NULL
		ORDER BY t.name, t.id
		LIMIT $2`, orgID, sectionLimit+1)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Tag, error) {
		var t crmcontracts.Tag
		var id, wsID ids.UUID
		if err := row.Scan(&id, &wsID, &t.Name, &t.Color, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt); err != nil {
			return t, err
		}
		t.Id = openapi_types.UUID(id)
		t.WorkspaceId = openapi_types.UUID(wsID)
		return t, nil
	})
}

// listMembershipsSection reads the lists the account belongs to, pruned to
// the ones the caller can read.
func listMembershipsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]crmcontracts.List, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	listScope, err := scopeClause(ctx, "list", "l", arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT l.id, l.workspace_id, l.name, l.entity_type, l.list_type, l.definition,
		       l.owner_id, l.team_id, l.created_at, l.updated_at, l.archived_at
		FROM list l
		JOIN list_member m ON m.list_id = l.id AND m.entity_type = 'organization' AND m.entity_id = $%d
		WHERE l.archived_at IS NULL AND (%s)
		ORDER BY l.name, l.id
		LIMIT %d`, orgPos, listScope, sectionLimit+1), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.List, error) {
		var l crmcontracts.List
		var id, wsID ids.UUID
		var ownerID, teamID *ids.UUID
		var entityType, listType string
		if err := row.Scan(&id, &wsID, &l.Name, &entityType, &listType, &l.Definition,
			&ownerID, &teamID, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt); err != nil {
			return l, err
		}
		l.Id = openapi_types.UUID(id)
		l.WorkspaceId = openapi_types.UUID(wsID)
		l.EntityType = crmcontracts.ListEntityType(entityType)
		l.ListType = crmcontracts.ListListType(listType)
		l.OwnerId = uuidPtr(ownerID)
		l.TeamId = uuidPtr(teamID)
		return l, nil
	})
}
