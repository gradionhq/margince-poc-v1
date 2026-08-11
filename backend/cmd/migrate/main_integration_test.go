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

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
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
	// Separate cleanups: a Fatalf in one (mustMigrate) must not skip the
	// other. LIFO order still drops the clone before its template.
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", tpl) })
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", clone) })

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

func TestDropDBTerminatesLingeringSessions(t *testing.T) {
	// Teardown runs right after a test process exits, when its backends may
	// not have gone yet — and drop_clone now propagates failures, so a drop
	// that could lose that race would fail the lane flakily. WITH (FORCE)
	// makes the drop win: the session dies, not the teardown.
	maint, base, withDB := testDSNs(t)
	name := base + "_verbs_lingering"
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", name) })

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", name)
	conn, err := pgx.Connect(context.Background(), withDB(name))
	if err != nil {
		t.Fatalf("opening the lingering session: %v", err)
	}
	// Deliberately not closed before the drop — the drop must win anyway.
	// The cleanup only releases the client-side socket afterwards: by then
	// the server has terminated the session, so Close may or may not report
	// an error depending on whether the conn noticed, and neither answer
	// carries signal.
	//craft:ignore swallowed-errors Close on a force-terminated session frees the client fd; its error is noise either way
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	mustMigrate(t, "drop-db", "--dsn", maint, "--name", name)
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", name); out != "false\n" {
		t.Fatalf("db-exists after force-dropping printed %q, want %q", out, "false\n")
	}
	if _, err := conn.Exec(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("the lingering session survived drop-db — WITH (FORCE) must terminate it")
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

func TestDBVerbsRejectNamesTheServerWouldTruncate(t *testing.T) {
	// Postgres truncates identifiers over 63 bytes to their prefix, so an
	// unchecked long --name would drop/create the database that owns that
	// prefix — a database the caller never named. The verbs must refuse
	// instead, before anything destructive runs.
	maint, base, withDB := testDSNs(t)
	prefix := base + "_verbs_trunc_"
	victim := prefix + strings.Repeat("x", 63-len(prefix)) // exactly the limit: accepted
	long := victim + "y"                                   // one byte over: its truncation IS victim
	ok := base + "_verbs_ok"                               // the over-length-template case's would-be output
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", victim) })
	// If the guard regresses, the template case creates `ok` right before the
	// test fails — drop it either way so the failure path cannot leak it
	// (dropping an absent database succeeds).
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", ok) })

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", victim)
	stamp(t, withDB(victim), "CREATE TABLE survivor_marker (id int)")

	for _, args := range [][]string{
		{"recreate-db", "--dsn", maint, "--name", long},
		{"drop-db", "--dsn", maint, "--name", long},
		{"db-exists", "--dsn", maint, "--name", long},
		{"recreate-db", "--dsn", maint, "--name", ok, "--template", long},
	} {
		if _, err := migrateCmd(t, args...); err == nil || !strings.Contains(err.Error(), "identifier limit") {
			t.Fatalf("migrate %s: got %v, want a refusal naming the identifier limit", strings.Join(args, " "), err)
		}
	}
	if !tableExists(t, withDB(victim), "survivor_marker") {
		t.Fatal("a verb given the over-length name touched the database owning its truncated prefix — the refusal must come before any destructive statement")
	}
	if out := mustMigrate(t, "db-exists", "--dsn", maint, "--name", ok); out != "false\n" {
		t.Fatalf("recreate-db with an over-length --template still created --name: db-exists printed %q, want %q", out, "false\n")
	}
}

func TestRecreateDBRefusesATemplateNamingTheDatabaseItself(t *testing.T) {
	// recreate-db drops before it creates, so --template equal to --name
	// would destroy the template and then copy from nothing. The refusal
	// must come before the drop: the existing database survives.
	maint, base, withDB := testDSNs(t)
	name := base + "_verbs_self_tpl"
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", name) })

	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", name)
	stamp(t, withDB(name), "CREATE TABLE survivor_marker (id int)")

	if _, err := migrateCmd(t, "recreate-db", "--dsn", maint, "--name", name, "--template", name); err == nil || !strings.Contains(err.Error(), "distinct template") {
		t.Fatalf("recreate-db with --template equal to --name: got %v, want a refusal asking for a distinct template", err)
	}
	if !tableExists(t, withDB(name), "survivor_marker") {
		t.Fatal("recreate-db with --template equal to --name dropped the database — the refusal must come before the destructive drop")
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

// TestUpAppliesAnExtensionNamespaceAndTheRiverIndex drives the whole `up`
// path over a fresh database, which is the only place three claims can be
// checked at once: an extension namespace lands its schema and records it in
// its OWN tracking table, the River workspace-arg index exists after River's
// migrator has created the table it indexes, and a second run of the same
// input applies nothing.
//
// The extension namespace is synthesized rather than taken from the composed
// set on purpose: no in-tree unit ships a migrations layer yet, so a test
// reading composition.Extensions() would pass over an empty slice and prove
// nothing. up() takes the namespaces as a parameter precisely so this can be
// exercised without one.
//
// The seam that leaves — that run() actually calls composition.Extensions()
// and hands the result here — is held by TestCompositionWiredOnlyFromCmd in
// backend/extensions_arch_test.go, which REQUIRES cmd/migrate/main.go to
// import the composition module, plus Go's unused-import rule, which makes an
// import that feeds nothing a compile error. Neither is a substitute for the
// other: this test proves the namespaces are applied, the arch test proves
// they are the composed set's.
func TestUpAppliesAnExtensionNamespaceAndTheRiverIndex(t *testing.T) {
	maint, base, withDB := testDSNs(t)
	name := base + "_up_ext"
	t.Cleanup(func() { mustMigrate(t, "drop-db", "--dsn", maint, "--name", name) })
	mustMigrate(t, "recreate-db", "--dsn", maint, "--name", name)

	ctx := context.Background()
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom: %v", err)
	}
	namespace, err := dbmigrate.NamespaceFor("up-probe")
	if err != nil {
		t.Fatalf("deriving the probe namespace: %v", err)
	}
	// The tenant shape a real unit's migration declares, so the apply is
	// exercised against SQL that references a core table rather than a
	// standalone CREATE TABLE that could pass in an empty database.
	probe := dbmigrate.Namespace{Name: namespace, Migrations: []dbmigrate.Migration{{
		Version: "0001",
		Name:    "probe",
		UpSQL: `CREATE TABLE ext.ext_up_probe_probe (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE)`,
		DownSQL: `DROP TABLE ext.ext_up_probe_probe`,
	}}}

	dsn := withDB(name)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the fresh database: %v", err)
	}
	defer func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("closing the migrator connection: %v", err)
		}
	}()

	var out bytes.Buffer
	if err := up(ctx, conn, dsn, core, custom, []dbmigrate.Namespace{probe}, &out); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out.String(), namespace+" (1 declared)") {
		t.Errorf("up did not name the extension namespace it applied; it printed %q", out.String())
	}
	if !tableExists(t, dsn, "ext.ext_up_probe_probe") {
		t.Fatal("the extension namespace's table is absent — up applied core+custom and silently skipped the extension lane")
	}
	assertRecorded(t, dsn, "schema_migrations_"+namespace, "0001")
	assertRiverWorkspaceArgIndex(t, dsn)

	// Idempotent: the second run must apply nothing at all, extension lane
	// included, and must not fail re-creating the index.
	out.Reset()
	if err := up(ctx, conn, dsn, core, custom, []dbmigrate.Namespace{probe}, &out); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if !strings.Contains(out.String(), "applied 0 core+custom+extension + 0 river") {
		t.Errorf("re-running up over a database at head printed %q, want a zero-applied summary", out.String())
	}
}

// assertRecorded proves the version landed in the namespace's OWN tracking
// table: an extension migration recorded against core's table would be
// reverted by a plain `migrate down`.
func assertRecorded(t *testing.T, dsn, table, version string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to read %s: %v", table, err)
	}
	defer func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("closing after reading %s: %v", table, err)
		}
	}()
	var found bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM "+table+" WHERE version = $1)", version,
	).Scan(&found); err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	if !found {
		t.Errorf("%s has no row for version %s — the lane applied without recording, so it would re-apply on every boot", table, version)
	}
}

// assertRiverWorkspaceArgIndex pins the post-River statement. It cannot be a
// core migration (river_job does not exist while the core lane runs, and
// dbmigrate wraps each migration in a transaction), so nothing but this test
// would notice it disappearing.
func assertRiverWorkspaceArgIndex(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to probe for the river index: %v", err)
	}
	defer func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("closing after the index probe: %v", err)
		}
	}()
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes
		    WHERE tablename = 'river_job' AND indexname = 'river_job_workspace_arg')`,
	).Scan(&exists); err != nil {
		t.Fatalf("probing for the river index: %v", err)
	}
	if !exists {
		t.Error("river_job_workspace_arg is absent — the per-workspace job fan-out and both job-health statements fall back to a sequential scan")
	}
}
