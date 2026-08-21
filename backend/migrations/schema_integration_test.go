// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

// Integration lane (make test-integration): exercises the real schema on
// Postgres 16 — apply/reverse/re-apply, the version bump (data-model §1.3a),
// audit_log append-only (§11), and the signal-visibility backfill. §1.3's
// ∅-query and GUC-unset-deny gates were the RLS mechanism's own proof and
// went with it in core 0217 (ADR-0091 §8); what stands in their place is
// rbacgate_test.go over platform/auth. Fails loudly when the database is
// missing rather than skipping (a skipped security gate looks exactly like
// a passing one).

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

// ownerDSN administers the throwaway test database; appDSNFmt is the
// non-owner runtime role RLS must bind.
func dsns(t *testing.T) (owner string, appFmt string) {
	t.Helper()
	owner = os.Getenv("MARGINCE_TEST_DSN")
	if owner == "" {
		t.Fatal("MARGINCE_TEST_DSN is not set — run `make db-up` and try again (integration tests fail loudly, they never skip)")
	}
	return owner, os.Getenv("MARGINCE_TEST_APP_DSN")
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to %s: %v", dsn, err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing %s connection: %v", dsn, err)
		}
	})
	return conn
}

func migrateAll(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(context.Background(), conn, core, custom); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// resetSchema drops everything so each test run starts clean.
//
// BOTH schemas the migrations own, not just public. Every clone is copied from
// the migrated template (scripts/lib-testdb.sh), so ext arrives already created
// and owned by margince_owner; leaving it would hand 0202 a schema it did not
// create, and its `GRANT USAGE ON SCHEMA ext` would then fail for a migration
// role that is not the owner. Dropping it is the same move public already gets,
// for the same reason: the role under test must build what it owns.
//
// CASCADE here, where the migration's own down half deliberately uses RESTRICT:
// this is a test reset primitive over a throwaway clone, and refusing to clear
// a leftover extension table would only strand the next run. RESTRICT belongs
// in 0213_ext_schema.down.sql, where the data is real.
func resetSchema(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS ext CASCADE`); err != nil {
		t.Fatalf("resetting the ext schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("re-granting schema usage: %v", err)
	}
}

func TestMigrations_applyReverseReapply(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}

	applied, err := dbmigrate.Up(ctx, conn, core)
	if err != nil {
		t.Fatalf("first up: %v", err)
	}
	if applied != len(core.Migrations) {
		t.Fatalf("applied %d, want %d", applied, len(core.Migrations))
	}

	// Idempotent: a second run applies nothing.
	applied, err = dbmigrate.Up(ctx, conn, core)
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if applied != 0 {
		t.Fatalf("re-run applied %d, want 0", applied)
	}

	// Rows FIRST, and specifically in the append-only ledgers. An empty schema
	// reverses through anything: a down half that backfills with an UPDATE
	// aborts on the ledgers' immutability trigger, and a FOR EACH ROW trigger
	// never fires on zero rows — so this suite would report a rollback that
	// cannot happen on any installation that has ever done anything.
	seedLedgerRowsForReversal(t, conn)

	// Every migration reverses (B-EP02.1b), then the schema re-applies.
	reverted, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations))
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if reverted != len(core.Migrations) {
		t.Fatalf("reverted %d, want %d", reverted, len(core.Migrations))
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("re-apply after full down: %v", err)
	}
}

// seedWorkspace inserts a workspace.
//
// It used to probe the catalog for `workspace.name` first, because the replay
// suites applied core only as far as a past release where that column was still
// NOT NULL. Those suites are gone with the history they replayed, and no
// reachable schema has the column, so the probe was a branch that could never be
// taken.
func seedWorkspace(t *testing.T, conn *pgx.Conn, slug string) string {
	t.Helper()
	var id string
	if err := conn.QueryRow(context.Background(),
		`INSERT INTO workspace (slug) VALUES ($1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatalf("seeding workspace %s: %v", slug, err)
	}
	return id
}

// withGUC runs fn in a transaction bound to a workspace, mirroring the
// production database.WithWorkspaceTx contract.
func withGUC(t *testing.T, conn *pgx.Conn, wsID string, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net: after the Commit below this rollback is a designed no-op, and fn's error already reached the caller
	defer func() { _ = tx.Rollback(ctx) }()
	if wsID != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
			t.Fatalf("set_config: %v", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestVersionBumpAndSkewSemantics(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	owner := connect(t, ownerDSN)
	headSchema(t, owner)
	ws := seedWorkspace(t, owner, "tenant-v")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	var version int64
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO person (full_name, source, captured_by) VALUES ('Vera', 'test', 'human:test') RETURNING id, version`,
		).Scan(&id, &version)
	}); err != nil {
		t.Fatalf("inserting person: %v", err)
	}
	if version != 1 {
		t.Fatalf("fresh row version = %d, want 1", version)
	}

	// The trigger bumps version on every UPDATE (data-model §1.3a).
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE person SET title = 'CTO' WHERE id = $1 RETURNING version`, id).Scan(&version)
	}); err != nil {
		t.Fatalf("updating person: %v", err)
	}
	if version != 2 {
		t.Fatalf("version after update = %d, want 2", version)
	}

	// The If-Match write shape: a stale version matches zero rows.
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE person SET title = 'CEO' WHERE id = $1 AND version = $2`, id, int64(1))
		if err != nil {
			t.Fatalf("stale update: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Error("stale If-Match version updated a row; must affect 0 → 409 version_skew")
		}
		return nil
	}); err != nil {
		t.Fatalf("stale-version tx: %v", err)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	owner := connect(t, ownerDSN)
	headSchema(t, owner)
	ws := seedWorkspace(t, owner, "tenant-audit")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			// entity_id is NOT NULL since 0075 (audit_log is record-mutations-only).
			`INSERT INTO audit_log (actor_type, actor_id, action, entity_type, entity_id)
			 VALUES ('human', 'human:test', 'create', 'person', uuidv7()) RETURNING id`).Scan(&id)
	}); err != nil {
		t.Fatalf("seeding an audit row: %v", err)
	}

	for _, stmt := range []string{
		`UPDATE audit_log SET actor_id = 'tampered' WHERE id = $1`,
		`DELETE FROM audit_log WHERE id = $1`,
	} {
		err := withGUC(t, app, ws, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, id)
			return err
		})
		var pgErr *pgconn.PgError
		if err == nil {
			t.Errorf("%q succeeded; audit_log must be append-only", stmt)
		} else if !errors.As(err, &pgErr) {
			t.Errorf("%q failed with %v, want a loud database error", stmt, err)
		}
	}
}

// seedLedgerRowsForReversal plants one row in each append-only ledger, at head,
// so the reversal above has something a row-level trigger can refuse.
//
// The two ledgers are the only tables in the schema that forbid UPDATE and
// DELETE outright, which makes them the only ones whose down half cannot
// backfill the ordinary way. That asymmetry is invisible against an empty
// table, and an empty table is what this suite used to reverse.
func seedLedgerRowsForReversal(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	// A workspace first, because that is the only state these rows exist in:
	// both ledgers are written inside a workspace-bound transaction, and the
	// foreign key the rollback restores would have refused a row without one.
	seedWorkspace(t, conn, "ledger-reversal")
	if _, err := conn.Exec(ctx, `
		INSERT INTO audit_log (actor_type, actor_id, action, entity_type, entity_id)
		VALUES ('system', 'system:reversal-fixture', 'create', 'workspace', gen_random_uuid())`); err != nil {
		t.Fatalf("seeding the audit_log row this reversal must survive: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO system_log (actor_type, actor_id, action)
		VALUES ('system', 'system:reversal-fixture', 'reversal_fixture')`); err != nil {
		t.Fatalf("seeding the system_log row this reversal must survive: %v", err)
	}
}
