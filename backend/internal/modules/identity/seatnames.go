// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Naming a seat.
//
// Its own file rather than a paragraph in users.go, which owns the seat
// LIFECYCLE — invite, reactivate, deactivate, re-role, each a governed write
// with an actor and an audit row behind it. This is a directory read: no
// actor, no gate of its own, nothing changed. The two share a table and
// nothing else.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SeatNames names the colleagues in a set, as a human would recognize them.
//
// It is a WORKSPACE read and carries no row scope, because a seat is not a
// record: app_user holds no owner_id and no capture privacy, and the surfaces
// that already name colleagues — who_knows, account_coverage — name them to any
// reader in the workspace. A row-scope clause here would be inventing a rule
// this table has never had.
//
// The workspace binding is the transaction's, so a colleague of another tenant
// cannot be named however the argument was built. A seat this workspace does
// not hold is simply absent from the answer, which the caller renders as the id
// it already has.
func (s *Service) SeatNames(ctx context.Context, seats []ids.UserID) (map[ids.UUID]string, error) {
	names := map[ids.UUID]string{}
	if len(seats) == 0 {
		return names, nil
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, display_name FROM app_user WHERE id = ANY($1) AND archived_at IS NULL`, seats)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			names[id] = name
		}
		return rows.Err()
	})
	return names, err
}
