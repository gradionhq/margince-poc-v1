// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// preservedResetTables are the workspace_id tables a reset must NOT delete:
// the identity/auth layer (so every user, including the admin, stays logged in
// and the org survives) and the append-only ledgers (their immutability trigger
// forbids DELETE, and operational history should outlive a data reset). Every
// other workspace_id table is domain/config data and is swept, then re-seeded.
var preservedResetTables = map[string]bool{
	"app_user": true, "role": true, "role_assignment": true,
	"team": true, "team_membership": true,
	"session": true, "passport": true, "auth_token": true,
	"audit_log": true, "system_log": true,
}

// resetTargetTables lists every public base table carrying a workspace_id
// column that is not preserved — derived from the catalog so a newly added
// tenant table is swept automatically rather than escaping a hand-kept list.
func resetTargetTables(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND c.relname NOT LIKE 'schema_migrations_%'
		  AND a.attname = 'workspace_id'
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !preservedResetTables[name] {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// sweepWorkspaceData deletes every row of the target tables for the bound
// workspace. Running as the non-superuser app role, it cannot disable FK
// triggers, so it discovers a safe order at runtime: each pass tries every
// still-populated table behind a savepoint and defers the ones a child FK still
// blocks to the next pass, until all are clear. A pass with no progress means an
// unbreakable FK cycle — surfaced explicitly, never silently skipped.
func sweepWorkspaceData(ctx context.Context, tx pgx.Tx, tables []string) error {
	remaining := append([]string(nil), tables...)
	for len(remaining) > 0 {
		var stuck []string
		progressed := false
		for _, t := range remaining {
			if _, err := tx.Exec(ctx, "SAVEPOINT reset_sp"); err != nil {
				return err
			}
			_, delErr := tx.Exec(ctx,
				`DELETE FROM `+pgx.Identifier{t}.Sanitize()+
					` WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
			if delErr == nil {
				if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT reset_sp"); err != nil {
					return err
				}
				progressed = true
				continue
			}
			if !isForeignKeyViolation(delErr) {
				return delErr
			}
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT reset_sp"); err != nil {
				return err
			}
			// A rollback leaves the savepoint defined but unusable for a
			// subsequent SAVEPOINT of the same name until it's released —
			// without this, repeated passes over a slow-to-clear table
			// would pile up shadowed savepoints for the life of the tx.
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT reset_sp"); err != nil {
				return err
			}
			stuck = append(stuck, t)
		}
		if !progressed {
			return fmt.Errorf("data reset: unresolved foreign-key cycle among %v", stuck)
		}
		remaining = stuck
	}
	return nil
}

// clearWorkspaceOutbox removes this workspace's staged events. event_outbox is
// infra-owned and has no workspace_id column — tenancy lives in the envelope —
// so the reset must not leave events that point at rows it just deleted.
func clearWorkspaceOutbox(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx,
		`DELETE FROM event_outbox WHERE envelope->>'workspace_id' = current_setting('app.workspace_id')`)
	return err
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
