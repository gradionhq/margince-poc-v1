// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The database-lifecycle verbs are the integration lane's clone machinery
// (scripts/lib-testdb.sh db_admin): recreate-db/drop-db own destructive
// DROP/CREATE DATABASE, and db-exists prints the literal answer the lane's
// ensure_template string-compares. These tests pin that contract against a
// real Postgres so a regression fails here, not as a broken lane.

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// testDSNs derives the verbs' maintenance-db target from the lane's owner
// DSN, plus the clone's own db name — the uniqueness prefix that keeps the
// databases these tests create from colliding with parallel packages on the
// shared cluster.
func testDSNs(t *testing.T) (maint string, base string, withDB func(string) string) {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN is not set — run `make db-up` and try again (integration tests fail loudly, they never skip)")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing MARGINCE_TEST_DSN: %v", err)
	}
	base = path.Base(u.Path)
	withDB = func(db string) string {
		v := *u
		v.Path = "/" + db
		return v.String()
	}
	return withDB("postgres"), base, withDB
}

func migrateCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), args, &out)
	return out.String(), err
}

func mustMigrate(t *testing.T, args ...string) string {
	t.Helper()
	out, err := migrateCmd(t, args...)
	if err != nil {
		t.Fatalf("migrate %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// stamp runs one statement on the named database and disconnects — the
// disconnect matters: CREATE DATABASE ... TEMPLATE refuses a template with
// live sessions.
func stamp(t *testing.T, dsn, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to stamp the database: %v", err)
	}
	_, execErr := conn.Exec(ctx, sql)
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("disconnecting after the stamp: %v", err)
	}
	if execErr != nil {
		t.Fatalf("executing %q: %v", sql, execErr)
	}
}

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to probe for table %s: %v", table, err)
	}
	var exists bool
	scanErr := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists)
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("disconnecting after the probe: %v", err)
	}
	if scanErr != nil {
		t.Fatalf("probing for table %s: %v", table, scanErr)
	}
	return exists
}

func TestRecreateDBCopiesTheTemplateAndStartsOverOnAnExistingDatabase(t *testing.T) {
	maint, base, withDB := testDSNs(t)
	tpl, clone := base+"_verbs_tpl", base+"_verbs_clone"
	t.Cleanup(func() {
		mustMigrate(t, "drop-db", "--dsn", maint, "--name", clone)
		mustMigrate(t, "drop-db", "--dsn", maint, "--name", tpl)
	})

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", tpl)
	// A marker table distinguishes a real template copy from a blank create.
	stamp(t, withDB(tpl), "CREATE TABLE template_marker (id int)")

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", clone, "--template", tpl)
	if !tableExists(t, withDB(clone), "template_marker") {
		t.Fatal("recreate-db --template produced a database without the template's table — it was not copied from the template")
	}

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", clone)
	if tableExists(t, withDB(clone), "template_marker") {
		t.Fatal("recreate-db kept the prior contents — it must drop the existing database before creating")
	}
}

func TestDropDBSucceedsWhenTheDatabaseIsAbsent(t *testing.T) {
	maint, base, _ := testDSNs(t)
	name := base + "_verbs_never_created"
	// drop_clone runs on every teardown path, including after a failed
	// create — dropping nothing must not be an error.
	mustMigrate(t, "drop-db", "--dsn", maint, "--name", name)
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "false\n" {
		t.Fatalf("db-exists after dropping an absent database printed %q, want %q", out, "false\n")
	}
}

func TestDBExistsPrintsTheLiteralAnswerTheLaneParses(t *testing.T) {
	// ensure_template string-compares this stdout — it is a wire contract
	// between the binary and scripts/lib-testdb.sh, not cosmetics.
	maint, base, _ := testDSNs(t)
	name := base + "_verbs_probe"
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", name) })

	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "false\n" {
		t.Fatalf("db-exists for an absent database printed %q, want %q", out, "false\n")
	}
	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", name)
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "true\n" {
		t.Fatalf("db-exists for a present database printed %q, want %q", out, "true\n")
	}
}

func TestDBVerbsQuoteNamesThatNeedIt(t *testing.T) {
	maint, base, _ := testDSNs(t)
	// Uppercase folds and the embedded quote breaks if the name is spliced
	// unquoted; the exact string must round-trip create → probe → drop.
	name := base + `_verbs_Ca"se`
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", name) })

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", name)
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "true\n" {
		t.Fatalf("db-exists after creating %q printed %q — the name did not survive quoting", name, out)
	}
	mustMigrate(t, "drop-db", "--dsn", maint, "--name", name)
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "false\n" {
		t.Fatalf("db-exists after dropping %q printed %q — the drop missed the quoted name", name, out)
	}
}

func TestDBVerbsRequireAName(t *testing.T) {
	maint, _, _ := testDSNs(t)
	for _, verb := range []string{"recreate-db", "drop-db", "db-exists"} {
		if _, err := migrateCmd(t, verb, "--dsn", maint); err == nil || !strings.Contains(err.Error(), "--name") {
			t.Fatalf("%s without --name: got %v, want an error naming the missing flag", verb, err)
		}
	}
}
