// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// A deployed installation's migration role owns the schema and is NOT a
// superuser. That single difference decides whether a migration's data half runs
// at all: tenant tables carry FORCE ROW LEVEL SECURITY, FORCE binds the owner,
// and FORCE does not reach a superuser or a BYPASSRLS role. The default dev and
// CI owner IS a superuser (the Postgres container's POSTGRES_USER), so an unbound
// tenant write lands there and reaches no row under an ordinary owner: the one
// difference that separates a migration which works from one that only appears to.
//
// This file supplies the role that tells the two apart, and states the mechanism
// once so the tests that rely on it are not each re-arguing it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// migratorRole holds NO exemption — not a superuser, no BYPASSRLS — while owning
// everything the migrations create. That is the whole point of it.
//
// A role is CLUSTER-scoped, not database-scoped, so a login role left behind by a
// test run is a standing credential on every database in that cluster, including
// the dev one; and this role owns the migrated tables, so it can drop their RLS
// policies outright. It is therefore created with a per-run random password and
// dropped again in Cleanup, and the name carries the pid so two concurrent
// packages cannot adopt each other's.
var migratorRole = fmt.Sprintf("margince_migrator_test_%d", os.Getpid())

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
	password := randomPassword(t)
	// Dropped and recreated rather than reused: a role left over from an earlier
	// run may have been granted anything in the meantime, and inheriting it would
	// quietly weaken every assertion that rests on what this role cannot do.
	for _, statement := range []string{
		`DROP ROLE IF EXISTS ` + migratorRole,
		`CREATE ROLE ` + migratorRole + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOBYPASSRLS`,
		`GRANT CREATE, USAGE ON SCHEMA public TO ` + migratorRole,
		// CREATE on the DATABASE, not just on public: since 0202_ext_schema the
		// migrations create a second schema (ext), and CREATE SCHEMA is a
		// database-level privilege. A deployed installation's migration role
		// already holds it and always has — scripts/deploy/db-bootstrap.sql
		// runs `CREATE DATABASE margince OWNER margince_owner`, and a database
		// owner holds CREATE on it implicitly — so this closes a gap between
		// the stand-in and the role it stands in for rather than widening what
		// the role may do. Nothing in this file's charter is loosened: the
		// privilege confers neither rolsuper nor rolbypassrls, which is the
		// one thing every test here rests on (assertNoRLSExemption).
		//
		// Contrast extensionsTheOperatorInstalls below, which stays an
		// out-of-band operator step for the opposite reason: `vector` is
		// untrusted and needs SUPERUSER, a privilege this role must never hold.
		`DO $$ BEGIN EXECUTE format('GRANT CREATE ON DATABASE %I TO %I', current_database(), '` + migratorRole + `'); END $$`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("preparing the %s role: %v", migratorRole, err)
		}
	}
	t.Cleanup(func() {
		// The role owns the migrated tables, so its objects go first. Left
		// behind, it would be a standing login on every database in the cluster
		// with the power to drop those tables' RLS policies.
		for _, statement := range []string{
			`DROP OWNED BY ` + migratorRole + ` CASCADE`,
			`DROP ROLE IF EXISTS ` + migratorRole,
		} {
			if _, err := admin.Exec(context.Background(), statement); err != nil {
				t.Errorf("removing the %s role (%s): %v — a login role left on this cluster owns the "+
					"migrated tables and can drop their tenant-isolation policies", migratorRole, statement, err)
			}
		}
	})
	for _, extension := range extensionsTheOperatorInstalls {
		if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+extension); err != nil {
			t.Fatalf("installing the %s extension as the operator would: %v", extension, err)
		}
	}

	config, err := pgx.ParseConfig(mustOwnerDSN(t))
	if err != nil {
		t.Fatalf("parsing the test DSN: %v", err)
	}
	config.User, config.Password = migratorRole, password
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

// randomPassword mints the run's credential. Hex of 16 random bytes: no quoting
// concerns in the CREATE ROLE literal, and nothing derivable from the repo.
func randomPassword(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("minting the migration role's password: %v", err)
	}
	return hex.EncodeToString(buf)
}

func mustOwnerDSN(t *testing.T) string {
	t.Helper()
	owner, _ := dsns(t)
	return owner
}

// The mechanism itself, stated as a test because the whole sweep of workspace
// loops through the migration tree rests on it: for the role that applies
// migrations on a deployed installation, an unbound UPDATE of a tenant table is
// not an error but a SUCCESSFUL no-op — the policy's USING clause hides every
// row rather than refusing the statement. That silence is why a lost backfill
// leaves no trace. (An unbound INSERT of literal rows is the loud case instead:
// WITH CHECK refuses it. The rule covers both; only this one needs proving,
// because only this one can pass unnoticed.)
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

// assertNotTheDatabaseOwner is the second half of what "the deployed migration
// role" means, and the half nothing checked until 0202 needed it.
//
// A database's owner holds every database-level privilege implicitly, so a
// stand-in that happened to own this database would satisfy CREATE SCHEMA
// without anyone having granted it — and the whole point of the role is that it
// must be given, explicitly, whatever the migrations need. Left unasserted, a
// future clone whose owner is this role would turn the fitness test below into a
// test of nothing.
func assertNotTheDatabaseOwner(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var owns bool
	if err := conn.QueryRow(ctx, `
		SELECT pg_get_userbyid(datdba) = current_user
		FROM pg_database WHERE datname = current_database()`).Scan(&owns); err != nil {
		t.Fatalf("reading the database owner: %v", err)
	}
	if owns {
		t.Fatalf("the migration role owns this database, so it holds every database-level privilege implicitly — "+
			"a migration needing one would pass here and fail on an installation where %s is a plain grantee", migratorRole)
	}
}

// TestTheCoreLaneAppliesUnderTheDeployedMigrationRole is a FITNESS test for a
// whole class of defect `make check-q` cannot see, and it exists because that
// class already shipped once.
//
// 0202 added `CREATE SCHEMA ext`. CREATE SCHEMA is a DATABASE-level privilege,
// not a schema-level one, and the restricted stand-in held only CREATE on
// public — so four tests in this package broke with `permission denied for
// database`. Nothing went red for the author: the merge gate does not run the
// integration lane, so every gate a task runs locally passed. The four that did
// break were about RLS, backfills and rollbacks; each named its own subject in
// its failure, and none of them said "your migration needs a privilege the
// deployed role was never granted".
//
// This says exactly that, and it is deliberately CHEAP — one reset, one
// migration pass, no assertions about content — rather than the alternative of
// putting the whole integration lane on the merge gate, which would change that
// gate's cost for every task in the repository forever. The bargain is that the
// signal arrives in the integration lane rather than at `make check-q`, but it
// arrives NAMED.
//
// A migration adding any other database-scoped statement (CREATE EXTENSION of an
// untrusted extension, CREATE DATABASE, an event trigger) fails here for the same
// reason, and the fix is the same shape: grant the stand-in what a deployed
// installation's migration role already holds — or, if a deployed one would not
// hold it either, move the statement to the operator's out-of-band step
// (extensionsTheOperatorInstalls).
func TestTheCoreLaneAppliesUnderTheDeployedMigrationRole(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	assertNotTheDatabaseOwner(ctx, t, migrator)

	// migrateAll and not a hand-rolled dbmigrate.Up: the lane under test is the
	// one cmd/migrate runs, embedded core plus custom, and a test applying some
	// other subset would go green over a tree it never touched.
	migrateAll(t, migrator)

	// The role really did build the schema — without this the test would pass on
	// a database somebody else had already migrated, which is the vacuous form of
	// every assertion above.
	var built int
	if err := migrator.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('public', 'ext')
		  AND c.relkind = 'r'
		  AND pg_get_userbyid(c.relowner) = current_user`).Scan(&built); err != nil {
		t.Fatalf("counting the tables the migration role owns: %v", err)
	}
	if built == 0 {
		t.Fatal("the migration role owns no table it created — the lane did not run as this role and this test proves nothing")
	}

	// The ext schema by name, because it is the statement that broke: a
	// table count alone would stay green if 0202 were reduced to a no-op.
	var extExists bool
	if err := migrator.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'ext')`).Scan(&extExists); err != nil {
		t.Fatalf("looking for the ext schema: %v", err)
	}
	if !extExists {
		t.Error("the ext schema does not exist after the core lane — 0202's CREATE SCHEMA is what needs a database-level privilege")
	}
}
