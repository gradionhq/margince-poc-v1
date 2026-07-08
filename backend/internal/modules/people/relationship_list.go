// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type ListRelationshipsInput struct {
	Kind            *string
	PersonID        *ids.PersonID
	OrganizationID  *ids.OrganizationID
	DealID          *ids.DealID
	IncludeArchived bool
	Limit           *int
	Cursor          string
}

func (s *Store) ListRelationships(ctx context.Context, in ListRelationshipsInput) ([]relationshipRow, storekit.Page, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)
	var out []relationshipRow
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where, err := relationshipListWhere(ctx, in, arg)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM relationship r WHERE %s ORDER BY r.id LIMIT $%d`,
			aliased(relationshipColumns, "r"), strings.Join(where, " AND "), arg(limit+1)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if out, err = scanRelationships(rows); err != nil {
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

// relationshipListWhere renders the list filters (kind/person/org/deal,
// archived, cursor) plus the endpoint-visibility scope into WHERE clauses,
// binding each value through arg.
func relationshipListWhere(ctx context.Context, in ListRelationshipsInput, arg func(any) int) ([]string, error) {
	where := []string{"true"}
	if in.Kind != nil {
		where = append(where, storekit.SQLf("r.kind = $%d", arg(*in.Kind)))
	}
	if in.PersonID != nil {
		where = append(where, storekit.SQLf("r.person_id = $%d", arg(*in.PersonID)))
	}
	if in.OrganizationID != nil {
		pos := arg(*in.OrganizationID)
		where = append(where, storekit.SQLf("(r.organization_id = $%d OR r.counterparty_org_id = $%d)", pos, pos))
	}
	if in.DealID != nil {
		where = append(where, storekit.SQLf("r.deal_id = $%d", arg(*in.DealID)))
	}
	if !in.IncludeArchived {
		where = append(where, "r.archived_at IS NULL")
	}
	if in.Cursor != "" {
		after, err := ids.Parse(in.Cursor)
		if err != nil {
			return nil, &RequiredFieldError{Field: "cursor"}
		}
		where = append(where, storekit.SQLf("r.id > $%d", arg(after)))
	}
	scope, err := relationshipEndpointScope(ctx, "r", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	return where, nil
}

// scanRelationships drains a relationship result set into rows.
func scanRelationships(rows pgx.Rows) ([]relationshipRow, error) {
	var out []relationshipRow
	for rows.Next() {
		rel, err := scanRelationship(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
