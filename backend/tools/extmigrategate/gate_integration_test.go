// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// One test per tenancy break. Each writes a real migration pair, runs the real
// gate against a real database as the real restricted role, and asserts that
// the refusal names the offending object — the whole value of this gate is that
// it says WHICH object broke WHICH rule, and a test that only checked for a
// non-nil error would let that decay silently.
//
// The breaks split into two families, and the split is the point of the design:
//
//   - Some never reach the catalog at all. A migration that writes a core
//     relation is refused by PostgreSQL inside the apply, because the ext_<name>
//     role holds no privilege on it. Those tests assert the database's own
//     message, not the gate's.
//   - The rest DO apply cleanly and are caught by the positive catalog
//     assertions afterwards. A table without workspace_id is perfectly legal
//     SQL; only the allowlist refuses it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// ownerDSN is the vanilla throwaway database this package migrates for itself
// in TestMain — NOT the clone of the shared template the integration lane hands
// it, which carries every enabled unit's extension tables and is therefore the
// one database this gate cannot run on. See throwawaydb_integration_test.go for
// why that is structural rather than tidiness. Fails loudly rather than
// skipping: a gate suite that skips reports green while proving nothing.
func ownerDSN(t *testing.T) string {
	t.Helper()
	if gateDSN == "" {
		t.Fatal("the throwaway gate database was not provisioned — run this package through `make test-it DIR=backend/tools/extmigrategate`")
	}
	return gateDSN
}

// unitName mints a unit name unique to this test and this process. The role
// the gate creates is CLUSTER-scoped and named from the unit, so two tests
// sharing a name would drop each other's role mid-run; the pid also keeps two
// worktrees on one cluster apart.
func unitName(t *testing.T, tag string) string {
	t.Helper()
	name := fmt.Sprintf("gate-%d-%s", os.Getpid(), tag)
	if len(name) > 32 {
		t.Fatalf("test unit name %q is %d characters — the grammar caps a unit name at 32", name, len(name))
	}
	return name
}

// namespaceOf is the same mapping the gate uses, so a test's SQL and the gate's
// expectations cannot drift.
func namespaceOf(t *testing.T, unit string) string {
	t.Helper()
	ns := "ext_" + strings.ReplaceAll(unit, "-", "_")
	return ns
}

// migrationDir writes one .up.sql/.down.sql pair and returns its directory.
func migrationDir(t *testing.T, up, down string) string {
	t.Helper()
	dir := t.TempDir()
	for name, sql := range map[string]string{"0001_gate.up.sql": up, "0001_gate.down.sql": down} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

// runGate is the whole gate, in-process. The command's own error is what a
// caller sees on stderr, so asserting on it is asserting on the real output.
func runGate(t *testing.T, unit, dir string) error {
	t.Helper()
	return run(context.Background(), unit, dir, ownerDSN(t))
}

// admin opens the owner connection tests use for the out-of-band setup an
// operator would do (installing an FDW, say) — never for anything the unit's
// own migrations are supposed to do.
func admin(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerDSN(t))
	if err != nil {
		t.Fatalf("connecting as the owner: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing the owner connection: %v", err)
		}
	})
	return conn
}

// requireRefusal asserts the gate refused AND that the refusal names the thing
// the author has to go fix.
func requireRefusal(t *testing.T, err error, mustMention ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the gate accepted the migration; it must refuse")
	}
	for _, want := range mustMention {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q — an author cannot act on it:\n%v", want, err)
		}
	}
}

// The fixtures are composed from four builders rather than written out per
// test, and every negative test differs from the scaffold in exactly ONE of
// them. Editing a shared string with strings.Replace was the alternative and it
// is the worse one: a replacement that silently misses leaves a test asserting
// a refusal it is no longer provoking.

// tenantColumnSQL is the correct tenant column, and the parameter the column
// tests vary.
const tenantColumnSQL = "workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE"

// predicateSQL is the canonical tenant predicate, as an author writes it.
const predicateSQL = `(workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)`

// noteTable builds the unit's one table with a caller-chosen tenant column.
// modifier carries UNLOGGED and the like.
func noteTable(ns, modifier, tenant string) string {
	return fmt.Sprintf(`CREATE %[2]sTABLE ext.%[1]s_note (
    id           uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    %[3]s,
    body         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
`, ns, modifier, tenant)
}

// notePolicy builds the table's single policy with a caller-chosen body, so a
// test can vary the predicate, the command, the roles or the permissiveness
// without touching anything else.
func notePolicy(ns, body string) string {
	return fmt.Sprintf("CREATE POLICY %[1]s_note_tenant_isolation ON ext.%[1]s_note %[2]s;\n", ns, body)
}

// noteRLS is ENABLE plus FORCE; the FORCE test passes force=false.
func noteRLS(ns string, force bool) string {
	sql := fmt.Sprintf("ALTER TABLE ext.%s_note ENABLE ROW LEVEL SECURITY;\n", ns)
	if force {
		sql += fmt.Sprintf("ALTER TABLE ext.%s_note FORCE ROW LEVEL SECURITY;\n", ns)
	}
	return sql
}

func noteGrant(ns string) string {
	return fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ext.%s_note TO margince_app;\n", ns)
}

// scaffoldUp is the shape a unit is meant to ship: one tenant table in ext,
// namespaced, workspace-scoped, forced RLS, one policy, DML to the app role.
func scaffoldUp(ns string) string {
	return noteTable(ns, "", tenantColumnSQL) +
		noteRLS(ns, true) +
		notePolicy(ns, "USING "+predicateSQL+" WITH CHECK "+predicateSQL) +
		noteGrant(ns)
}

func scaffoldDown(ns string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS ext.%s_note;\n", ns)
}

func TestGateAcceptsTheScaffoldedShape(t *testing.T) {
	unit := unitName(t, "ok")
	ns := namespaceOf(t, unit)
	dir := migrationDir(t, scaffoldUp(ns), scaffoldDown(ns))

	if err := runGate(t, unit, dir); err != nil {
		t.Fatalf("the scaffolded shape must pass: %v", err)
	}

	// The gate promises to leave nothing behind — it is run against a
	// throwaway, but a leaked LOGIN role is a standing credential on the whole
	// cluster, and the throwaway is on the same cluster as everything else.
	var roles int
	if err := admin(t).QueryRow(context.Background(),
		`SELECT count(*) FROM pg_roles WHERE rolname = $1`, ns).Scan(&roles); err != nil {
		t.Fatalf("checking for a leaked role: %v", err)
	}
	if roles != 0 {
		t.Errorf("the gate left the %s login role on the cluster; it owns the tables it created and can drop their policies", ns)
	}
}

// Indexing workspace_id is the natural thing to do under RLS — the policy's
// predicate is a comparison on it, so every query filters on it — and it is the
// case the plan probe is most likely to get wrong. On a freshly created, empty,
// un-ANALYZEd table the planner's default statistics favour the index, so the
// predicate renders as `Index Cond:` and never as `Filter:`. That is exactly the
// state the gate sees right after applying a migration.
func TestGateAcceptsATenantTableIndexedOnWorkspaceID(t *testing.T) {
	unit := unitName(t, "idx")
	ns := namespaceOf(t, unit)
	up := scaffoldUp(ns) + fmt.Sprintf("CREATE INDEX %[1]s_note_ws ON ext.%[1]s_note (workspace_id);\n", ns)

	if err := runGate(t, unit, migrationDir(t, up, scaffoldDown(ns))); err != nil {
		t.Fatalf("a correctly isolated table that indexes %s must pass: %v", tenantColumn, err)
	}
}

// The plan probe's whole job is to refuse a connection row-level security does
// not bind, and the trap it guards is that such a connection reports
// relforcerowsecurity=true just like a bound one. This drives the probe directly
// with a SUPERUSER connection — the dev and CI owner, the exact Stage A trap —
// over a table deliberately NAMED so that the bare string "workspace_id" appears
// in the plan without the predicate binding anything.
//
// A probe that looked for the column name rather than the rendered predicate
// passes this. That is the false-pass the exact match exists to close, and this
// is the test that keeps it closed.
func TestTheRLSProbeRefusesAConnectionRowLevelSecurityDoesNotBind(t *testing.T) {
	ctx := context.Background()
	owner := admin(t)

	// Not created through the gate: the point is to hold the table's shape
	// correct and vary only the connection the probe runs over.
	name := fmt.Sprintf("ext_probe_%d_workspace_id_map", os.Getpid())
	for _, statement := range []string{
		fmt.Sprintf(`CREATE TABLE ext.%s (id uuid NOT NULL, workspace_id uuid NOT NULL)`, name),
		fmt.Sprintf(`ALTER TABLE ext.%s ENABLE ROW LEVEL SECURITY`, name),
		fmt.Sprintf(`ALTER TABLE ext.%s FORCE ROW LEVEL SECURITY`, name),
		fmt.Sprintf(`CREATE POLICY %s_iso ON ext.%s USING %s WITH CHECK %s`, name, name, predicateSQL, predicateSQL),
	} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatalf("building the probe fixture (%s): %v", statement, err)
		}
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TABLE IF EXISTS ext.`+name); err != nil {
			t.Errorf("dropping the probe fixture: %v — every later gate run enumerates schema ext", err)
		}
	})

	var super bool
	if err := owner.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		t.Fatalf("reading the owner's attributes: %v", err)
	}
	if !super {
		t.Fatalf("this cluster's owner is not a superuser, so FORCE binds it and the probe would pass — the trap being tested does not exist here")
	}

	rel := relation{name: name}
	if err := owner.QueryRow(ctx, `
		SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`, extSchema, name).Scan(&rel.oid); err != nil {
		t.Fatalf("locating the probe fixture: %v", err)
	}

	requireRefusal(t, assertRLSBindsTheOwner(ctx, owner, rel, extSchema+"."+name),
		"row-level security is not binding the owner", name)
}

func TestGateRejectsTableWithoutWorkspaceID(t *testing.T) {
	unit := unitName(t, "nows")
	ns := namespaceOf(t, unit)
	up := fmt.Sprintf(`CREATE TABLE ext.%[1]s_thing (id uuid NOT NULL PRIMARY KEY);`, ns)
	down := fmt.Sprintf(`DROP TABLE IF EXISTS ext.%s_thing;`, ns)

	err := runGate(t, unit, migrationDir(t, up, down))
	requireRefusal(t, err, "ext."+ns+"_thing", "workspace_id")
}

// A policy that admits every row is the failure this whole tier exists to
// prevent, and it is invisible to every textual rule: the SQL is well-formed,
// the table is namespaced, RLS is on and forced. Only comparing the rendered
// predicate against the one canonical spelling catches it.
func TestGateRejectsPermissivePolicy(t *testing.T) {
	unit := unitName(t, "perm")
	ns := namespaceOf(t, unit)
	up := noteTable(ns, "", tenantColumnSQL) +
		noteRLS(ns, true) +
		notePolicy(ns, "USING (true) WITH CHECK (true)") +
		noteGrant(ns)

	err := runGate(t, unit, migrationDir(t, up, scaffoldDown(ns)))
	requireRefusal(t, err, ns+"_note_tenant_isolation", "USING true")
}

// ENABLE without FORCE is the subtlest of the seven: the table looks protected
// and is, for the app role — but not for its OWNER, which is the unit's own
// ext_<name> role, and that is the role the unit's own later migrations run as.
func TestGateRejectsMissingForceRLS(t *testing.T) {
	unit := unitName(t, "force")
	ns := namespaceOf(t, unit)
	up := noteTable(ns, "", tenantColumnSQL) +
		noteRLS(ns, false) +
		notePolicy(ns, "USING "+predicateSQL+" WITH CHECK "+predicateSQL) +
		noteGrant(ns)

	err := runGate(t, unit, migrationDir(t, up, scaffoldDown(ns)))
	requireRefusal(t, err, "ext."+ns+"_note", "relforcerowsecurity=false")
}

// The core relations are not the extension's to touch, and this test asserts
// the DATABASE says so rather than the gate: the refusal arrives from inside
// the apply, before any catalog assertion runs. That is the difference applying
// as ext_<name> makes — as the dev/CI owner (a superuser) this INSERT would
// succeed and would have to be detected afterwards, which needs a rule per way
// of writing a core table.
func TestGateRejectsDMLOnCoreRelations(t *testing.T) {
	unit := unitName(t, "dml")
	ns := namespaceOf(t, unit)
	up := scaffoldUp(ns) + `
INSERT INTO person (workspace_id, full_name, source, captured_by)
SELECT id, 'smuggled', 'gate', 'gate' FROM workspace;
`
	err := runGate(t, unit, migrationDir(t, up, scaffoldDown(ns)))
	requireRefusal(t, err, "permission denied for table person", "0001_gate.up.sql")
}

// relkind 'f' and 'm' cannot carry row-level security at all: PostgreSQL
// accepts neither ENABLE nor FORCE on them, so a policy on such a relation is
// not merely absent, it is impossible. Skipping them in the enumeration — the
// obvious reading of "check the tables" — is therefore the silent hole, and
// both are rejected outright.
func TestGateRejectsForeignTable(t *testing.T) {
	ctx := context.Background()
	owner := admin(t)

	// The operator's step, out of band, exactly as installing an extension is:
	// the gate itself grants the unit nothing here. USAGE goes to PUBLIC because
	// the gate drops and recreates the ext_<name> role, so a grant to the role
	// would not survive.
	server := fmt.Sprintf("gate_fdw_%d", os.Getpid())
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS postgres_fdw`,
		fmt.Sprintf(`CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host 'localhost', dbname 'nowhere')`, server),
		fmt.Sprintf(`GRANT USAGE ON FOREIGN SERVER %s TO PUBLIC`, server),
	} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatalf("preparing the foreign server (%s): %v", statement, err)
		}
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP SERVER IF EXISTS `+server+` CASCADE`); err != nil {
			t.Errorf("dropping the foreign server: %v", err)
		}
	})

	t.Run("foreign table", func(t *testing.T) {
		unit := unitName(t, "ftab")
		ns := namespaceOf(t, unit)
		up := fmt.Sprintf(`CREATE FOREIGN TABLE ext.%[1]s_remote (id uuid, workspace_id uuid) SERVER %[2]s OPTIONS (table_name 'anything');`, ns, server)
		down := fmt.Sprintf(`DROP FOREIGN TABLE IF EXISTS ext.%s_remote;`, ns)

		err := runGate(t, unit, migrationDir(t, up, down))
		requireRefusal(t, err, "ext."+ns+"_remote", "FOREIGN TABLE", "row-level security")
	})

	t.Run("materialized view", func(t *testing.T) {
		unit := unitName(t, "mview")
		ns := namespaceOf(t, unit)
		up := scaffoldUp(ns) + fmt.Sprintf(
			"CREATE MATERIALIZED VIEW ext.%[1]s_digest AS SELECT workspace_id, count(*) AS n FROM ext.%[1]s_note GROUP BY 1;\n", ns)
		down := fmt.Sprintf("DROP MATERIALIZED VIEW IF EXISTS ext.%s_digest;\n", ns) + scaffoldDown(ns)

		err := runGate(t, unit, migrationDir(t, up, down))
		requireRefusal(t, err, "ext."+ns+"_digest", "MATERIALIZED VIEW", "row-level security")
	})
}

// The namespace is what keeps two units sharing the ext schema from colliding
// or addressing each other's rows, so an unprefixed name is a tenancy break
// between EXTENSIONS rather than between workspaces.
func TestGateRejectsObjectOutsideNamespace(t *testing.T) {
	unit := unitName(t, "outns")
	ns := namespaceOf(t, unit)
	up := scaffoldUp(ns) + "CREATE TABLE ext.other_thing (id uuid NOT NULL PRIMARY KEY);\n"
	down := "DROP TABLE IF EXISTS ext.other_thing;\n" + scaffoldDown(ns)

	err := runGate(t, unit, migrationDir(t, up, down))
	requireRefusal(t, err, "ext.other_thing", "outside the unit's namespace")
}

// connectAs opens a session as an arbitrary role on the same database. Only
// the concurrency test needs it: to prove the gate refuses a role another run
// is holding, something has to hold one.
func connectAs(t *testing.T, user, password string) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(ownerDSN(t))
	if err != nil {
		t.Fatalf("parsing the test DSN: %v", err)
	}
	config.User, config.Password = user, password
	conn, err := pgx.ConnectConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("connecting as %s: %v", user, err)
	}
	return conn
}
