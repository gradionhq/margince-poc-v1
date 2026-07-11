// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ListInput narrows the catalog read: Object is required (the admin
// field table is always per-object); Status of active/retired selects
// one lifecycle state, empty returns both — this admin view deliberately
// does NOT default-exclude retired rows (CUSTOM-FIELDS-WIRE-1), because
// a retired field's slug stays reserved and the admin table is the one
// surface that still shows it.
type ListInput struct {
	Object string
	Status *string
	Cursor *string
	Limit  *int
}

// predicate renders the WHERE clause and its arguments after validating
// the closed object/status vocabularies — split from List to keep each
// half inside the lint's complexity budget.
func (in ListInput) predicate() (string, []any, error) {
	if !allowedObjects[in.Object] {
		return "", nil, &ValidationError{Errors: []FieldError{{Field: fieldObject, Code: "unsupported_object"}}}
	}
	status := ""
	if in.Status != nil {
		status = *in.Status
	}
	if status != "" && status != statusActive && status != statusRetired {
		return "", nil, &ValidationError{Errors: []FieldError{{Field: fieldStatus, Code: "unsupported_status"}}}
	}

	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := storekit.SQLf("object = $%d", arg(in.Object))
	if status != "" {
		where += storekit.SQLf(" AND status = $%d", arg(status))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return "", nil, err
		}
		where += storekit.SQLf(" AND (created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID))
	}
	return where, args, nil
}

// List reads the catalog page for one (workspace, object) under the
// contract's default -created_at,id keyset order. Workspace-shared admin
// config: the object read grant is the authority, RLS pins the tenant,
// and there is no row scope to compose (the pipeline precedent).
func (s *Service) List(ctx context.Context, in ListInput) ([]crmcontracts.CustomField, storekit.Page, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	where, args, err := in.predicate()
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	fields := []crmcontracts.CustomField{}
	var page storekit.Page
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+catalogColumns+` FROM custom_field WHERE `+where+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanCustomField(rows)
			if err != nil {
				return err
			}
			fields = append(fields, f)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(fields) > limit {
			fields = fields[:limit]
			last := fields[len(fields)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if err != nil {
		return nil, storekit.Page{}, err
	}
	return fields, page, nil
}
