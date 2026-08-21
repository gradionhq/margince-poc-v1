// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

// headSchema is for the tests in this package that want the schema the
// migrations BUILD, not the act of building it.
//
// Most suites here rewind and replay: they drop everything, apply a prefix,
// assert on what a repair or a rollback did. That is why this package is carved
// out of backend/integrationmigrateonce_test.go's migrate-once rule, and it
// stays carved out. But roughly a quarter of its tests only read the head
// catalog — is this column generated, is that view security_invoker, does every
// row-scoped FK carry a visibility decision — and they were taking the carve-out
// too, paying a DROP SCHEMA CASCADE and a full replay of every embedded
// migration (~1.3s each) to arrive at a schema the lane had already handed them:
// every clone is copied from an already-migrated template.
//
// So: build head ONCE per process, then verify it is still head before each
// later use and rebuild only when it is not. The verification is what makes this
// safe to mix with the rewinding suites in the same binary — they run on the
// same database, and one of them leaves it two migrations short.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// headFingerprint is the catalog of the schema this process built at head,
// captured the first time headSchema ran. Empty until then.
//
// A digest of the catalog, not the migration ledger's version set. The ledger
// records a version, not a checksum — scripts/lib-testdb.sh says so where it
// decides whether to reuse the template — so a version set answers "which
// migrations were applied" and cannot answer "is the schema still what they
// build". Both questions have to come out right here: a rewinding suite that
// leaves the ledger at head having altered a table would pass a version check
// and hand the next reader a schema nothing in this package built.
var headFingerprint string

// headRebuilds counts the full drop-and-replay rebuilds headSchema has done, so
// its own test can tell the cheap path from the expensive one. Nothing else
// distinguishes them: both leave a database at head with no rows in it, which is
// exactly why a helper that quietly rebuilt every time would go unnoticed and
// give back the whole saving.
var headRebuilds int

// headSchema hands the caller a database at head with no rows in it.
//
// The FIRST call in the process rebuilds unconditionally — drop, then replay
// every embedded migration — so the reference catalog is one this binary's own
// migrations produced rather than whatever the template happened to hold. That
// closes the case the template cannot: the ledger cannot see an edited or
// renumbered migration, so a clone built from a template that diverged from this
// checkout is at "head" by the only measure the database keeps.
//
// Later calls compare the live catalog against that reference and rebuild only
// on a mismatch, which is what the rewinding suites in this package produce.
// Matching costs one query instead of a full replay.
//
// Data is emptied by testdb.Reset either way, so a caller may write rows; it
// gets the same clean slate a rebuild gave it.
func headSchema(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	if headFingerprint == "" || catalogDigest(t, conn) != headFingerprint {
		resetSchema(t, conn)
		migrateAll(t, conn)
		headFingerprint = catalogDigest(t, conn)
		headRebuilds++
	}
	if err := testdb.Reset(ctx, conn); err != nil {
		t.Fatalf("emptying the head schema: %v", err)
	}
}

// catalogDigest is one md5 over the schema the migrations build, at the grain the
// tests that ride it actually read.
//
// That last clause is the whole design, and getting it wrong is worse than
// having no digest at all — a fingerprint blind to the objects its readers care
// about still READS as verification. So the contents are derived from what the
// head-only tests in this package assert on, one line each:
//
//   - columns, types, nullability — every one of them, and the baseline
//   - column defaults and generated expressions — schema_fitness asserts that
//     amount_minor_base is database-generated, and a column keeping its name,
//     type and nullability while losing its GENERATED clause is precisely the
//     move a rewind makes
//   - view definitions and reloptions — schema_fitness asserts a rollup view is
//     security_invoker, which lives in reloptions and in nothing else here
//   - triggers AND the function bodies they execute — the version bump and
//     audit_log's append-only guard are trigger behaviour, asserted by two
//     tests. Both halves, because pg_get_triggerdef prints which function a
//     trigger calls and not what that function does, and a migration that
//     changes nothing but a function body is a shape this tree ships
//   - table grants — the two behaviour tests above connect as the app role, so
//     a revoked grant changes what they observe while disturbing no object
//   - indexes and constraints — asserted directly by the cascade-index and FK
//     visibility tests
//   - the privilege and isolation surface no behaviour test here reaches, but a
//     migration can widen in one line: SECURITY DEFINER and search_path on a
//     function, EXECUTE grants, schema-level grants, default privileges, and
//     row-level security state including every policy. None of these change an
//     object's shape, so a projection built from shapes alone would call
//     `GRANT CREATE ON SCHEMA public TO margince_app` a no-op
//   - NOT extension-owned objects. pgvector alone installs ~109 functions into
//     public, and the image is digest-pinned: carrying them would make a
//     Renovate bump of that digest read as the migrations having changed head
//
// A schema with no objects at all digests to the empty string rather than to
// SQL NULL, so a caller arriving after a suite that dropped the schema and left
// it dropped reads a mismatch and rebuilds — the outcome it needs — instead of
// failing on a scan.
func catalogDigest(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var digest string
	err := conn.QueryRow(context.Background(),
		catalogParts+`
		SELECT COALESCE(md5(string_agg(part, E'\n' ORDER BY part)), '') FROM parts`).Scan(&digest)
	if err != nil {
		t.Fatalf("fingerprinting the schema catalog: %v", err)
	}
	return digest
}

// catalogParts is the schema surface both readers of the catalog share: the
// digest above, and the committed projection headcatalog_integration_test.go
// compares against.
//
// ONE definition, deliberately. The digest answers "is this database still the
// one my process built" and the projection answers "is that schema the one the
// repository expects"; a second copy of the query would let the two questions
// drift apart, and the failure mode is silent — whichever list lost an object
// class still reads as verification. The doc comment on catalogDigest says which
// object classes are here and why each one earns its line.
const catalogParts = `
		WITH tracked AS (
			-- The migration ledgers are excluded from every arm a table can
			-- appear in. They are
			-- how the runner records what it applied, not schema any migration
			-- builds: dbmigrate owns their shape, so a change there — the
			-- content_digest column, say — would read as the migrations having
			-- altered head. Which is also why the two readers of this query
			-- want them gone. The digest asks "is this the schema my process
			-- built" and the committed projection asks "is it the one the
			-- repository expects"; neither question is about the ledger.
			SELECT c.oid
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND c.relname LIKE 'schema\_migrations\_%'
		), parts AS (
			SELECT n.nspname || '.' || c.relname || '.' || a.attname || ' ' ||
			       format_type(a.atttypid, a.atttypmod) ||
			       CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END ||
			       ' gen=' || COALESCE(NULLIF(a.attgenerated::text, ''), '-') ||
			       ' def=' || COALESCE(pg_get_expr(ad.adbin, ad.adrelid), '-') AS part
			  FROM pg_attribute a
			  JOIN pg_class c ON c.oid = a.attrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			  LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
			 WHERE n.nspname IN ('public', 'ext')
			   AND c.relkind IN ('r', 'p', 'v', 'm')
			   AND c.oid NOT IN (SELECT oid FROM tracked)
			   AND a.attnum > 0
			   AND NOT a.attisdropped
			UNION ALL
			SELECT n.nspname || '.' || c.relname || ' opts=' ||
			       COALESCE(array_to_string(c.reloptions, ','), '-') || ' ' ||
			       COALESCE(pg_get_viewdef(c.oid), '-')
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND c.relkind IN ('v', 'm')
			UNION ALL
			SELECT n.nspname || '.' || c.relname || '.' || t.tgname || ' ' ||
			       pg_get_triggerdef(t.oid)
			  FROM pg_trigger t
			  JOIN pg_class c ON c.oid = t.tgrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND NOT t.tgisinternal
			UNION ALL
			SELECT n.nspname || '.' || p.proname || '(' ||
			       pg_get_function_identity_arguments(p.oid) || ')' ||
			       ' secdef=' || p.prosecdef::text ||
			       ' cfg=' || COALESCE(array_to_string(p.proconfig, ','), '-') ||
			       ' acl=' || COALESCE(array_to_string(p.proacl, ','), '-') ||
			       ' ' || p.prosrc
			  FROM pg_proc p
			  JOIN pg_namespace n ON n.oid = p.pronamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND NOT EXISTS (SELECT 1 FROM pg_depend d
			                    WHERE d.objid = p.oid AND d.deptype = 'e')
			UNION ALL
			SELECT n.nspname || '.' || c.relname || ' acl=' ||
			       COALESCE(array_to_string(c.relacl, ','), '-') ||
			       ' rls=' || c.relrowsecurity::text || ' force=' || c.relforcerowsecurity::text
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
			   AND c.oid NOT IN (SELECT oid FROM tracked)
			UNION ALL
			SELECT 'policy ' || n.nspname || '.' || c.relname || '.' || pol.polname ||
			       ' cmd=' || pol.polcmd::text || ' permissive=' || pol.polpermissive::text ||
			       ' qual=' || COALESCE(pg_get_expr(pol.polqual, pol.polrelid), '-') ||
			       ' check=' || COALESCE(pg_get_expr(pol.polwithcheck, pol.polrelid), '-')
			  FROM pg_policy pol
			  JOIN pg_class c ON c.oid = pol.polrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			UNION ALL
			SELECT 'schema ' || n.nspname || ' acl=' ||
			       COALESCE(array_to_string(n.nspacl, ','), '-')
			  FROM pg_namespace n
			 WHERE n.nspname IN ('public', 'ext')
			UNION ALL
			SELECT 'defacl ' || COALESCE(n.nspname, '-') || ' objtype=' ||
			       d.defaclobjtype::text || ' acl=' || array_to_string(d.defaclacl, ',')
			  FROM pg_default_acl d
			  LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
			UNION ALL
			SELECT n.nspname || '.' || c.relname || '.' || con.conname || ' ' ||
			       pg_get_constraintdef(con.oid)
			  FROM pg_constraint con
			  JOIN pg_class c ON c.oid = con.conrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('public', 'ext')
			   AND c.oid NOT IN (SELECT oid FROM tracked)
			UNION ALL
			SELECT schemaname || '.' || indexname || ' ' || indexdef
			  FROM pg_indexes
			 WHERE schemaname IN ('public', 'ext')
			   AND tablename NOT LIKE 'schema\_migrations\_%'
		)`

// The helper's own two claims, which nothing else in this package can make:
// a database still at head is not rebuilt, and one that is NOT still at head is.
// Both matter, and they fail in opposite directions — rebuilding always gives up
// the entire saving silently, rebuilding never hands a rewound schema to a test
// that asserts on the head catalog and blames the migration it was reading.
func TestHeadSchemaDoesNotRebuildADatabaseAlreadyAtHead(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)

	headSchema(t, conn)
	settled := headRebuilds

	headSchema(t, conn)
	if headRebuilds != settled {
		t.Fatalf("headSchema rebuilt a database already at head (%d rebuilds, want %d) — "+
			"every caller pays a full replay for a schema it was already handed", headRebuilds, settled)
	}
}

// schemaMove is one way a suite in this package can leave the schema off head
// while the migration ledger still reads head. Each names an object class the
// digest claims to cover, and each is DERIVED from the live catalog rather than
// hardcoded: a named object the migrations later rename turns into a
// missing-object error that reads like a broken helper, and the coverage it was
// standing for goes quietly.
var schemaMoves = []struct {
	name string
	// find returns the statement that moves the schema, or "" when this schema
	// has no such object to move.
	find func(t *testing.T, conn *pgx.Conn) string
}{
	{"an index is dropped", func(t *testing.T, conn *pgx.Conn) string {
		return scanOne(t, conn, `
			SELECT 'DROP INDEX ' || quote_ident(schemaname) || '.' || quote_ident(indexname)
			  FROM pg_indexes
			 WHERE schemaname = 'public'
			   AND indexname NOT IN (SELECT conname FROM pg_constraint)
			 ORDER BY indexname LIMIT 1`)
	}},
	{"a trigger is dropped", func(t *testing.T, conn *pgx.Conn) string {
		return scanOne(t, conn, `
			SELECT 'DROP TRIGGER ' || quote_ident(t.tgname) || ' ON ' ||
			       quote_ident(n.nspname) || '.' || quote_ident(c.relname)
			  FROM pg_trigger t
			  JOIN pg_class c ON c.oid = t.tgrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND NOT t.tgisinternal
			 ORDER BY t.tgname LIMIT 1`)
	}},
	{"a view stops being security_invoker", func(t *testing.T, conn *pgx.Conn) string {
		return scanOne(t, conn, `
			SELECT 'ALTER VIEW ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname) ||
			       ' SET (security_invoker = false)'
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND c.relkind = 'v'
			   AND array_to_string(c.reloptions, ',') LIKE '%security_invoker=true%'
			 ORDER BY c.relname LIMIT 1`)
	}},
	{"a trigger function's body is replaced", func(t *testing.T, conn *pgx.Conn) string {
		// The class a dropped trigger CANNOT stand in for: the trigger
		// definition names its function and says nothing about what that
		// function does, and this tree ships migrations that change only a body.
		return scanOne(t, conn, `
			SELECT 'CREATE OR REPLACE FUNCTION ' || quote_ident(n.nspname) || '.' ||
			       quote_ident(p.proname) || '() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN RETURN NEW; END $body$'
			  FROM pg_proc p
			  JOIN pg_namespace n ON n.oid = p.pronamespace
			  JOIN pg_trigger t ON t.tgfoid = p.oid AND NOT t.tgisinternal
			 WHERE n.nspname = 'public'
			   AND pg_get_function_identity_arguments(p.oid) = ''
			 ORDER BY p.proname LIMIT 1`)
	}},
	{"a grant is revoked", func(t *testing.T, conn *pgx.Conn) string {
		return scanOne(t, conn, `
			SELECT 'REVOKE SELECT ON ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname) ||
			       ' FROM margince_app'
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND c.relkind = 'r'
			   AND array_to_string(c.relacl, ',') LIKE '%margince_app=%r%'
			 ORDER BY c.relname LIMIT 1`)
	}},
	{"a column loses its default", func(t *testing.T, conn *pgx.Conn) string {
		return scanOne(t, conn, `
			SELECT 'ALTER TABLE ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname) ||
			       ' ALTER COLUMN ' || quote_ident(a.attname) || ' DROP DEFAULT'
			  FROM pg_attrdef ad
			  JOIN pg_class c ON c.oid = ad.adrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			  JOIN pg_attribute a ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum
			 WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attgenerated = ''
			 ORDER BY c.relname, a.attname LIMIT 1`)
	}},
}

func TestHeadSchemaRebuildsWhateverTheDigestClaimsToWatch(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)

	for _, move := range schemaMoves {
		t.Run(move.name, func(t *testing.T) {
			headSchema(t, conn)
			settled := headRebuilds

			stmt := move.find(t, conn)
			if stmt == "" {
				t.Fatalf("this schema has no object to move for %q — the digest still claims to cover that class, "+
					"so either the class is gone and the digest should say so, or this query stopped finding it", move.name)
			}
			if _, err := conn.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("moving the schema off head with %q: %v", stmt, err)
			}

			headSchema(t, conn)
			if headRebuilds != settled+1 {
				t.Fatalf("headSchema did not rebuild after %q (%d rebuilds, want %d) — "+
					"the ledger still reads head, so the digest is the only thing that can see this", move.name, headRebuilds, settled+1)
			}
			if catalogDigest(t, conn) != headFingerprint {
				t.Fatal("the rebuilt catalog does not match the head this process built")
			}
		})
	}
}

// scanOne runs a query expected to return at most one text value, answering ""
// when it returns no row.
func scanOne(t *testing.T, conn *pgx.Conn, sql string) string {
	t.Helper()
	var out string
	switch err := conn.QueryRow(context.Background(), sql).Scan(&out); {
	case errors.Is(err, pgx.ErrNoRows):
		return ""
	case err != nil:
		t.Fatalf("finding an object to move: %v", err)
	}
	return out
}
