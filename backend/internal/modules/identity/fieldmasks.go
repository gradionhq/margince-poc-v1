// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// loadFieldMasks reads the columns the principal's roles withhold — the
// union over every role held, additive like the grants: a role that masks a
// field masks it for the seat, whatever another role says.
func loadFieldMasks(ctx context.Context, tx pgx.Tx, roleKeys []string) ([]principal.FieldMask, error) {
	if len(roleKeys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT object, field, condition FROM field_mask WHERE role_key = ANY($1) ORDER BY object, field, condition`,
		roleKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var masks []principal.FieldMask
	for rows.Next() {
		var m principal.FieldMask
		if err := rows.Scan(&m.Object, &m.Field, &m.Condition); err != nil {
			return nil, err
		}
		masks = append(masks, m)
	}
	return masks, rows.Err()
}
