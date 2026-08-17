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

// preservedResetTables are the tables a reset must NOT delete. Everything else
// in the public schema is domain/config data: swept, then re-seeded.
//
// This list is the whole definition, and it did not used to be. The sweep
// derived its targets from the presence of a workspace_id column, which was a
// proxy for "holds this tenant's data" — a proxy ADR-0091 §8 phase D is
// removing table by table. Under it, a module that dropped the column silently
// stopped being reset: consent_purpose was the first, and the reset then failed
// re-seeding purposes it had not deleted. So the derivation is inverted. What a
// reset must keep is a decision someone has to make; what it deletes follows.
//
// Four kinds are kept:
//
//   - identity and auth, so every user (the admin above all) stays logged in
//     and the installation survives its own reset;
//   - the append-only ledgers, whose immutability trigger forbids DELETE and
//     whose operational history should outlive a data reset;
//   - installation configuration and secrets, which are not this workspace's
//     records — a reset that cleared them would leave an installation unable
//     to reach the providers and mailboxes it is still configured for;
//   - the job runtime, which is River's to manage and not ours to truncate
//     underneath a running worker.
var preservedResetTables = map[string]bool{
	// identity and auth
	objectWorkspace: true, "app_user": true, "role": true, "role_assignment": true,
	"team": true, "team_membership": true,
	"session": true, "passport": true, "auth_token": true,
	// append-only ledgers
	"audit_log": true, "system_log": true,
	// installation configuration and secrets
	"setting": true, "vault_secret": true, "ai_call_config": true,
	"embed_store_binding": true,
	// The derived channel-provider registry: installation-global reference data,
	// not this workspace's records, on the SAME footing as `setting` above — a
	// reset that cleared it would leave the installation unable to recognise the
	// connectors it has compiled in, and the sweep's unconditional,
	// non-tenant-scoped DELETE would hit the activity_kind_fkey and
	// activity_channel_provider_fkey constraints and abort the whole sweep
	// transaction outright.
	"activity_kind": true, "channel_provider": true,
	// in-flight delivery: drained by the outbox pass, not deleted under it
	"event_outbox": true,
	// The retention floor's evidence (A165, migration 0289). Preserved from the
	// sweep's own DELETE because a direct one is REFUSED outright: the row goes
	// only with the activity it substantiates, through the FK's CASCADE, and
	// its guard tells the two apart by whether the parent is already gone.
	// Sweeping it directly would abort the reset transaction on the first row.
	//
	// It is still cleared by a reset — `activity` is swept, and the cascade takes
	// the evidence with it. Preserved here means "not a target", never "kept".
	"activity_retention_evidence": true,
}

// providerCredentialRefs collects the sealed API keys the provider
// connections hold, BEFORE the sweep deletes the rows naming them. Same
// reasoning as collectWorkspaceSecretRefs: the ciphertext lives in a table
// with no workspace_id, so these handles are the only thing that will still
// connect it to this installation once the rows are gone.
func providerCredentialRefs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT credential_ref FROM provider_connection WHERE credential_ref IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("data reset: reading provider credential handles: %w", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("data reset: reading a provider credential handle: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("data reset: reading provider credential handles: %w", err)
	}
	return refs, nil
}

// resetTargetTables lists every public base table a reset sweeps: all of them,
// less the preserved set above, the migration ledgers, and River's own schema.
// Derived from the catalog so a newly added table is swept automatically rather
// than escaping a hand-kept list — the burden of naming falls on what must be
// KEPT, where forgetting an entry fails loudly (the admin loses their session,
// the installation loses its config) instead of quietly leaving a tenant's rows
// behind after a reset that reported success.
func resetTargetTables(ctx context.Context, tx pgx.Tx) ([]resetTarget, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname,
		       EXISTS (SELECT 1 FROM pg_attribute a
		                WHERE a.attrelid = c.oid AND a.attname = 'workspace_id'
		                  AND a.attnum > 0 AND NOT a.attisdropped) AS tenant_scoped
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND c.relname NOT LIKE 'schema_migrations%'
		  AND c.relname NOT LIKE 'river_%'
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resetTarget
	for rows.Next() {
		var t resetTarget
		if err := rows.Scan(&t.name, &t.tenantScoped); err != nil {
			return nil, err
		}
		if !preservedResetTables[t.name] {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// resetTarget is one table to sweep and whether it still carries the tenant
// column. A table that has one keeps its predicate: phase D is mid-flight, and
// the two spellings agree on every row an installation with one live workspace
// holds (ADR-0061 §3), so the narrower one costs nothing and keeps a reset from
// widening ahead of the schema. The predicate goes when the last column does.
type resetTarget struct {
	name         string
	tenantScoped bool
}

// deleteStatement is the sweep's DELETE for one target.
func (t resetTarget) deleteStatement() string {
	stmt := `DELETE FROM ` + pgx.Identifier{t.name}.Sanitize()
	if t.tenantScoped {
		stmt += ` WHERE workspace_id = current_setting('app.workspace_id')::uuid`
	}
	return stmt
}

// sweepWorkspaceData deletes every row of the target tables for the bound
// workspace. Running as the non-superuser app role, it cannot disable FK
// triggers, so it discovers a safe order at runtime: each pass tries every
// still-populated table behind a savepoint and defers the ones a child FK still
// blocks to the next pass, until all are clear. A pass with no progress means an
// unbreakable FK cycle — surfaced explicitly, never silently skipped.
func sweepWorkspaceData(ctx context.Context, tx pgx.Tx, tables []resetTarget) error {
	remaining := append([]resetTarget(nil), tables...)
	for len(remaining) > 0 {
		var stuck []resetTarget
		progressed := false
		for _, t := range remaining {
			if _, err := tx.Exec(ctx, "SAVEPOINT reset_sp"); err != nil {
				return err
			}
			_, delErr := tx.Exec(ctx, t.deleteStatement())
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

// clearWorkspaceOutbox removes the staged events, so the reset does not leave
// events pointing at rows it just deleted.
//
// event_outbox is infra-owned and has no workspace_id column. Tenancy used to
// live in the envelope, and the delete matched on it; the envelope carries no
// tenant now (ADR-0091 §6), and under one installation there is no other
// tenant's event to spare — every staged row belongs to the installation being
// reset.
func clearWorkspaceOutbox(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM event_outbox`)
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

// collectWorkspaceSecretRefs reads every sealed-credential handle in the
// installation, BEFORE the sweep deletes the rows that name them.
//
// It has to run first because vault_secret is deliberately not swept: it
// carries no RLS (migrations/core/0062), since the tenant lives inside the ref
// and inside the AES-256-GCM AAD. The sweep therefore never sees it, and a
// reset that did not collect these first would leave the ciphertext resident
// and unreachable forever — credential material outliving the wipe that was
// supposed to clear it.
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
				` WHERE `+pgx.Identifier{vaultRefColumn}.Sanitize()+` IS NOT NULL`)
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

// tablesWithVaultRef lists the tables holding a credential handle, derived from
// the catalog for the same reason resetTargetTables is: a new one enrols itself.
//
// Holding the COLUMN is the whole test. It used to also require a workspace_id,
// which was the same set only while every connection table had one — and phase
// D (ADR-0091 §8) is removing it table by table, so each connection table that
// dropped it silently stopped being collected and left its sealed credential
// resident after a reset. That is the same failure resetTargetTables already
// had for the same reason, and it is why neither derivation may ask about a
// column that is on its way out.
func tablesWithVaultRef(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND a.attname = $1 AND a.attnum > 0 AND NOT a.attisdropped
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
