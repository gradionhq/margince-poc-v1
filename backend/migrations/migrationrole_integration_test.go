// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// A deployed installation's migration role owns the schema and is NOT a
// superuser. That single difference decides whether a migration's data half runs
// at all: tenant tables carry FORCE ROW LEVEL SECURITY, FORCE binds the owner,
// and a superuser bypasses RLS outright. The dev and CI owner IS a superuser (the
// Postgres container's POSTGRES_USER), so every migration in this repo appeared
// to work while an unbound tenant write silently matched zero rows in production.
//
// This file supplies the role that tells the two apart, and states the mechanism
// once so the tests that rely on it are not each re-arguing it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The migrator's own credentials. It exists only inside the throwaway test
// database's cluster, and its whole purpose is to hold NO exemption: not a
// superuser, no BYPASSRLS, but the owner of everything the migrations create.
const (
	migratorRole     = "margince_migrator_test"
	migratorPassword = "margince_migrator_test"
)

// extensionsTheOperatorInstalls are created by the cluster operator out of band
// (a superuser step: an init container in the deployed stack, `make db-up`
// locally), never by the migration role. The migrations ask for them with IF NOT
// EXISTS, so pre-creating them here is what lets a non-superuser apply the tree —
// exactly as it happens on a real installation.
var extensionsTheOperatorInstalls = []string{"vector", "btree_gist", "unaccent", "pg_trgm"}

// asMigrator prepares a freshly reset schema for a non-superuser owner and
// returns a connection as that role. The admin connection stays available for
// what an operator or the app would do — seeding rows, inspecting results.
func asMigrator(t *testing.T, admin *pgx.Conn) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '` + migratorRole + `') THEN
				CREATE ROLE ` + migratorRole + ` LOGIN PASSWORD '` + migratorPassword + `'
					NOSUPERUSER NOBYPASSRLS;
			END IF;
		END $$`,
		`GRANT CREATE, USAGE ON SCHEMA public TO ` + migratorRole,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("preparing the %s role: %v", migratorRole, err)
		}
	}
	for _, extension := range extensionsTheOperatorInstalls {
		if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+extension); err != nil {
			t.Fatalf("installing the %s extension as the operator would: %v", extension, err)
		}
	}

	config, err := pgx.ParseConfig(mustOwnerDSN(t))
	if err != nil {
		t.Fatalf("parsing the test DSN: %v", err)
	}
	config.User, config.Password = migratorRole, migratorPassword
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connecting as %s: %v", migratorRole, err)
	}
	t.Cleanup(func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("closing the %s connection: %v", migratorRole, err)
		}
	})
	assertNoRLSExemption(ctx, t, conn)
	return conn
}

// assertNoRLSExemption refuses to let the role silently acquire the exemption it
// exists to lack. Without this, granting it superuser one day — or running the
// suite on a cluster where it already is one — would turn every test built on it
// into a test of nothing at all.
func assertNoRLSExemption(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var super, bypass bool
	if err := conn.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass); err != nil {
		t.Fatalf("reading the migration role's attributes: %v", err)
	}
	if super || bypass {
		t.Fatalf("the migration role holds rolsuper=%t rolbypassrls=%t, so row-level security does "+
			"not bind it and every assertion resting on this connection proves nothing", super, bypass)
	}
}

func mustOwnerDSN(t *testing.T) string {
	t.Helper()
	owner, _ := dsns(t)
	return owner
}

// The mechanism itself, stated as a test because the whole sweep of workspace
// loops through the migration tree rests on it: for the role that applies
// migrations on a deployed installation, an unbound tenant write is not an error
// but a SUCCESSFUL no-op. That is why nothing failed while a backfill was lost.
func TestAnUnboundTenantWriteSucceedsAndChangesNothingForTheMigrationRole(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	migrateAll(t, migrator)

	workspace := seedWorkspace(t, admin, "unbound-write")
	seedRole(t, admin, workspace, "admin", true, []byte(`{"objects":{},"row_scope":"all"}`))

	unbound, err := migrator.Exec(ctx, `UPDATE role SET name = 'renamed' WHERE key = 'admin'`)
	if err != nil {
		t.Fatalf("the unbound write is expected to succeed and do nothing, not to fail: %v", err)
	}
	if rows := unbound.RowsAffected(); rows != 0 {
		t.Errorf("the unbound write touched %d rows; row-level security must hide all of them, or "+
			"this database's owner holds an exemption a deployed installation's does not", rows)
	}

	if _, err := migrator.Exec(ctx, `DO $$
		DECLARE ws uuid;
		BEGIN
			FOR ws IN SELECT id FROM workspace LOOP
				PERFORM set_config('app.workspace_id', ws::text, true);
				UPDATE role SET name = 'renamed' WHERE key = 'admin';
			END LOOP;
		END $$`); err != nil {
		t.Fatalf("the workspace-bound write failed: %v", err)
	}

	var name string
	if err := admin.QueryRow(ctx,
		`SELECT name FROM role WHERE workspace_id = $1 AND key = 'admin'`, workspace,
	).Scan(&name); err != nil {
		t.Fatalf("reading the role back: %v", err)
	}
	if name != "renamed" {
		t.Errorf("the workspace-bound write left name %q; binding app.workspace_id is what every "+
			"migration in the tree relies on to reach tenant rows", name)
	}
}
