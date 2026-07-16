// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// Integration lane (make test-integration): exercises the real schema on
// Postgres 16 — apply/reverse/re-apply, and the four blocking RLS gates
// from data-model §1.3: ∅-query, GUC-unset deny (read AND write), the
// version bump (§1.3a), and audit_log append-only (§11). Fails loudly when
// the database is missing rather than skipping (a skipped security gate
// looks exactly like a passing one).

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
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
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	custom, err := Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(context.Background(), conn, core, custom); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// resetSchema drops everything so each test run starts clean.
func resetSchema(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
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

	core, err := Core()
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

// TestAttachmentScanStatusBackfill proves 0070's judgment call directly:
// a database that already held attachment rows when the scan machinery
// arrived keeps those rows downloadable (backfilled 'clean' — they were
// uploaded under the no-scanner regime), while rows inserted after the
// migration start at the gate's default 'scanning'.
func TestAttachmentScanStatusBackfill(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}

	// Rewind to just before 0070 so the pre-existing row is inserted into
	// the pre-scan schema, exactly like a production database at 0069.
	scanIdx := -1
	for i, m := range core.Migrations {
		if m.Version == "0070" {
			scanIdx = i
			break
		}
	}
	if scanIdx < 0 {
		t.Fatal("core migrations contain no 0070 — the scan-status migration is missing")
	}
	if _, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations)-scanIdx); err != nil {
		t.Fatalf("down to pre-0070: %v", err)
	}

	ws := seedWorkspace(t, conn, "pre-scan")
	var existing string
	if err := conn.QueryRow(ctx, `
		INSERT INTO attachment (workspace_id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, 'person', uuidv7(), 'legacy.pdf', 'ws/legacy', 'upload', 'human:test')
		RETURNING id`, ws).Scan(&existing); err != nil {
		t.Fatalf("seeding a pre-migration attachment: %v", err)
	}

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("re-applying 0070 over existing rows: %v", err)
	}

	var status string
	if err := conn.QueryRow(ctx,
		`SELECT scan_status FROM attachment WHERE id = $1`, existing).Scan(&status); err != nil {
		t.Fatalf("reading backfilled scan_status: %v", err)
	}
	if status != "clean" {
		t.Errorf("pre-existing row scan_status = %q, want 'clean' (backfill must not brick old downloads)", status)
	}

	if err := conn.QueryRow(ctx, `
		INSERT INTO attachment (workspace_id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, 'person', uuidv7(), 'new.pdf', 'ws/new', 'upload', 'human:test')
		RETURNING scan_status`, ws).Scan(&status); err != nil {
		t.Fatalf("inserting a post-migration attachment: %v", err)
	}
	if status != "scanning" {
		t.Errorf("new row scan_status = %q, want the 'scanning' default", status)
	}

	// The CHECK constraint holds the closed vocabulary.
	if _, err := conn.Exec(ctx, `
		INSERT INTO attachment (workspace_id, entity_type, entity_id, filename, storage_key, source, captured_by, scan_status)
		VALUES ($1, 'person', uuidv7(), 'x', 'ws/x', 'upload', 'human:test', 'bogus')`, ws); err == nil {
		t.Error("scan_status outside (scanning|clean|blocked) was accepted; the CHECK must reject it")
	}
}

func seedWorkspace(t *testing.T, conn *pgx.Conn, slug string) string {
	t.Helper()
	var id string
	err := conn.QueryRow(context.Background(),
		`INSERT INTO workspace (name, slug, base_currency) VALUES ($1, $1, 'EUR') RETURNING id`,
		slug).Scan(&id)
	if err != nil {
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

func TestRLS_tenantIsolationGates(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN is not set — the RLS gates must run as the non-owner runtime role")
	}
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)

	wsA := seedWorkspace(t, owner, "tenant-a")
	wsB := seedWorkspace(t, owner, "tenant-b")

	app := connect(t, appDSN)
	ctx := context.Background()

	insertPerson := func(wsID, name string) error {
		return withGUC(t, app, wsID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'test', 'human:test')`,
				wsID, name)
			return err
		})
	}
	if err := insertPerson(wsA, "Ada A"); err != nil {
		t.Fatalf("insert into tenant A: %v", err)
	}
	if err := insertPerson(wsB, "Ben B"); err != nil {
		t.Fatalf("insert into tenant B: %v", err)
	}

	// ∅-query: tenant A's GUC sees none of tenant B's rows.
	if err := withGUC(t, app, wsA, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person`).Scan(&n); err != nil {
			t.Fatalf("count under tenant A: %v", err)
		}
		if n != 1 {
			t.Errorf("tenant A sees %d persons, want exactly its own 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A read tx: %v", err)
	}

	// GUC-unset: a connection with no workspace reads ZERO rows...
	if err := withGUC(t, app, "", func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person`).Scan(&n); err != nil {
			t.Fatalf("count with unset GUC: %v", err)
		}
		if n != 0 {
			t.Errorf("unset GUC sees %d rows, want 0 (deny-on-unset, never wildcard)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("unset-GUC read tx: %v", err)
	}

	// ...and cannot write (WITH CHECK).
	err := withGUC(t, app, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Eve', 'test', 'human:test')`,
			wsA)
		return err
	})
	if err == nil {
		t.Error("insert with unset GUC succeeded; RLS WITH CHECK must reject it")
	}

	// Cross-tenant write: tenant B's GUC cannot insert a tenant-A row.
	err = withGUC(t, app, wsB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Mallory', 'test', 'human:test')`,
			wsA)
		return err
	})
	if err == nil {
		t.Error("cross-tenant insert succeeded; WITH CHECK must reject it")
	}
}

func TestVersionBumpAndSkewSemantics(t *testing.T) {
	ownerDSN, appDSN := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ws := seedWorkspace(t, owner, "tenant-v")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	var version int64
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO person (workspace_id, full_name, source, captured_by) VALUES ($1, 'Vera', 'test', 'human:test') RETURNING id, version`,
			ws).Scan(&id, &version)
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
	resetSchema(t, owner)
	migrateAll(t, owner)
	ws := seedWorkspace(t, owner, "tenant-audit")

	app := connect(t, appDSN)
	ctx := context.Background()

	var id string
	if err := withGUC(t, app, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			// entity_id is NOT NULL since 0075 (audit_log is record-mutations-only).
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id)
			 VALUES ($1, 'human', 'human:test', 'create', 'person', uuidv7()) RETURNING id`, ws).Scan(&id)
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
