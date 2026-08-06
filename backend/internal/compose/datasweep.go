// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a data reset does to Postgres: which tables it sweeps, the order it
// discovers at runtime, the outbox drain, the overlay-mode revert, and the
// cf_* column drop that runs on the owner pool afterwards. datareset.go holds
// the transport and the orchestration that calls these; datareset_runtime.go
// holds the non-Postgres surfaces.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// clearOutbox drains the staged events in a transaction of its OWN, before the
// stream purge, rather than inside the sweep's transaction after it.
//
// The relay is not the job fleet: quiescing the queues does not stop it shipping
// event_outbox rows onto the streams. Staged rows left in place while the
// streams are purged are re-published seconds later into streams that were just
// emptied, and a subscriber then works events against rows the sweep is deleting.
//
// It narrows the window rather than closing it. The relay claims a batch FOR
// UPDATE SKIP LOCKED, so this DELETE waits on whatever is in flight — but that
// batch has already XADDed, and rows any concurrent request commits afterwards
// still reach the bus. The residual is one relay batch wide, not seconds wide.
//
// Running in its own transaction has a price the sweep's did not: a reset that
// fails later has already dropped this workspace's staged events, and they are
// never delivered.
func (h dataResetHandlers) clearOutbox(ctx context.Context) error {
	return database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		return clearWorkspaceOutbox(ctx, tx)
	})
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// vaultRefColumn is the one spelling every table uses for a keyvault
// handle (connector_connection, channel_connection, incumbent_connection).
// Deriving the collection from the catalog on this name rather than listing
// those three means a connection table added later is covered the day its
// column exists, which a hand-kept list would not be.
const vaultRefColumn = "credential_ref"

// collectWorkspaceSecretRefs reads every sealed-credential handle this
// workspace owns, BEFORE the sweep deletes the rows that name them.
//
// It has to run first because vault_secret is deliberately not a tenant table:
// it carries no workspace_id and no RLS (migrations/core/0062), since the
// tenant lives inside the ref and inside the AES-256-GCM AAD. The sweep
// therefore never sees it, and a reset that did not collect these first would
// leave the ciphertext resident and unreachable forever — credential material
// outliving the wipe that was supposed to clear it.
func collectWorkspaceSecretRefs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	tables, err := tablesWithVaultRef(ctx, tx)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, t := range tables {
		rows, err := tx.Query(ctx,
			`SELECT `+pgx.Identifier{vaultRefColumn}.Sanitize()+
				` FROM `+pgx.Identifier{t}.Sanitize()+
				` WHERE workspace_id = current_setting('app.workspace_id')::uuid
				  AND `+pgx.Identifier{vaultRefColumn}.Sanitize()+` IS NOT NULL`)
		if err != nil {
			return nil, fmt.Errorf("data reset: reading credential handles from %s: %w", t, err)
		}
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				rows.Close()
				return nil, fmt.Errorf("data reset: reading a credential handle from %s: %w", t, err)
			}
			refs = append(refs, ref)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("data reset: reading credential handles from %s: %w", t, err)
		}
	}
	return refs, nil
}

// tablesWithVaultRef lists the workspace-scoped tables holding a
// credential handle, derived from the catalog for the same reason
// resetTargetTables is: a new one enrols itself.
func tablesWithVaultRef(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		JOIN pg_attribute w ON w.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND a.attname = $1 AND a.attnum > 0 AND NOT a.attisdropped
		  AND w.attname = 'workspace_id' AND w.attnum > 0 AND NOT w.attisdropped
		ORDER BY c.relname`, vaultRefColumn)
	if err != nil {
		return nil, fmt.Errorf("data reset: listing tables holding a credential handle: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// dropResetCustomFieldColumns drops every runtime cf_* column via the owner
// pool (the ONE sanctioned ALTER TABLE chokepoint). DROP COLUMN CASCADE also
// removes each column's generated cf_<slug>_check constraint.
func dropResetCustomFieldColumns(ctx context.Context, schemaPool *pgxpool.Pool) error {
	rows, err := schemaPool.Query(ctx, `
		SELECT quote_ident(table_name), quote_ident(column_name)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE 'cf\_%'`)
	if err != nil {
		return err
	}
	type col struct{ table, name string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.name); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cols {
		if _, err := schemaPool.Exec(ctx, `ALTER TABLE `+c.table+` DROP COLUMN `+c.name+` CASCADE`); err != nil {
			return err
		}
	}
	return nil
}
