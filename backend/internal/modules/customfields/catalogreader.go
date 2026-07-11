// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// ActiveColumns is this module's half of the fieldcatalog cross-module
// seam (shared/ports/fieldcatalog): the catalog READ a record store
// (people, deals, …) needs to drive its cf_* columns, exposed through a
// port those stores can depend on without importing this module
// directly (ADR-0054 §3). Compose wires the concrete *Service in; the
// stores themselves see only fieldcatalog.Reader.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// var _ fieldcatalog.Reader = (*Service)(nil) documents the seam at its
// provider — the compile-time assertion itself lives in the compose
// integration suite (customfields_fieldcatalog_integration_test.go),
// which is where a cross-module wiring defect would otherwise first
// surface.

// ActiveColumns answers the active custom-field columns for one object,
// scoped to the workspace bound to ctx, ordered by column_name (a stable,
// deterministic order for SELECT/INSERT column-list building).
//
// Deliberately runs no auth.Require: this is called from inside a record
// store's Get/List/Create/Update, whose own RBAC gate already ran before
// the store reaches for its custom columns. What ActiveColumns exposes —
// which cf_* columns exist and their type — is workspace-visible schema
// (the same shape the admin catalog list already answers to anyone
// holding custom_field:read), not row data a second gate would need to
// narrow; the row-level RBAC/RLS the calling store enforces is what
// actually protects the values stored in those columns.
func (s *Service) ActiveColumns(ctx context.Context, object string) ([]fieldcatalog.Column, error) {
	var cols []fieldcatalog.Column
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT column_name, type FROM custom_field WHERE object = $1 AND status = $2 ORDER BY column_name`,
			object, statusActive)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c fieldcatalog.Column
			if err := rows.Scan(&c.Name, &c.Type); err != nil {
				return err
			}
			cols = append(cols, c)
		}
		return rows.Err()
	})
	return cols, err
}
