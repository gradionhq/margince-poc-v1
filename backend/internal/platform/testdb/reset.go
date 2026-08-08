// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package testdb is the integration lane's fast schema-reset helper. The
// integration suites need a clean database per test, and the obvious way to get
// one — DROP SCHEMA + re-run every embedded migration on each Setup — dominated
// the lane: the heaviest package alone remigrated ~180 times (~0.8s each). This
// package splits the cost. EnsureSchema migrates ONCE per test-binary process
// and records the empty schema's physical size; every later test in that process
// rides the already-migrated schema and only resets the data. Correctness holds
// because no migration seeds reference data a test depends on — the only
// data-touching migration (person_social backfill) is a no-op on an empty
// database.
//
// Emptying rows does not revert schema changes, so the reset also drops the
// runtime cf_ columns the customfields engine adds — see dropCustomFieldColumns.
//
// The reset stays safe under the lane's -p 1: within a package process tests
// run serially, so nothing races the shared connection between Reset and the
// next test. That covers tests, not goroutines a test leaves running — the
// DELETE batch takes row locks rather than TRUNCATE's whole-table ones, so a
// straggling writer is no longer locked out, and a suite that leaks one must
// stop it in cleanup. Across packages, each go-test binary is its own process (and, under
// the parallel runner, its own throwaway database), so migrateOnce is genuinely
// per-database.
package testdb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

var (
	migrateOnce sync.Once
	migrateErr  error

	// schemaReady reports that EnsureSchema has migrated this process's database.
	// Pool gates on it: a connection opened before the migration's DROP SCHEMA
	// holds descriptions of relations that no longer exist, and a shared pool
	// would carry that for the whole package rather than one test.
	schemaReady atomic.Bool

	// emptySizes is the physical size of every table on the freshly migrated,
	// still-empty schema, keyed by qualified name. reclaimBloat measures growth
	// against it — see there for why an absolute size threshold cannot work.
	// Atomic because EnsureSchema writes it, every later Reset reads it, and
	// baselineNewTables replaces it mid-run — Reset never calls EnsureSchema, so
	// there is no happens-before edge a plain map could rely on.
	emptySizes atomic.Pointer[map[string]int64]
)

// resetTables is the ONE spelling of which relations a reset acts on: ordinary
// tables in the schemas below, minus the schema_migrations_* ledger. That ledger
// is preserved so EnsureSchema's once-per-process contract holds — re-running
// dbmigrate.Up in a later process (parallel runner, fresh clone) must still see
// an unmigrated database, while a reset leaves the applied-version rows intact
// for the current one. Every caller selects a qualified identifier from it, so
// the emitted statements never depend on search_path resolution.
//
// ext is in scope for exactly the reason public is. Since 0198 every extension
// unit's tables live there (ADR-0069), applied by the same lane, and an ext_
// table left out of this fragment is one no reset ever empties: the rows an
// integration test writes through a unit survive into every later test in the
// process, and the failure surfaces somewhere else entirely as a flake. The
// omission is invisible while extensions/ is empty, which is precisely why it is
// closed BEFORE the first unit ships tables rather than after.
const resetTables = `
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname IN ('public', 'ext')
	  AND c.relkind = 'r'
	  AND c.relname NOT LIKE 'schema_migrations_%'`

// reclaimSlack is how much a table may grow past its empty size before a reset
// TRUNCATEs it instead of DELETEing it. Growth, not absolute size, is the
// signal: see reclaimBloat.
const reclaimSlack = 256 << 10

// execQuerier is the pgx subset each reset step needs, so every step can run
// inside Reset's single transaction rather than racing it on the bare
// connection.
type execQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// EnsureSchema migrates the test database exactly once per process. The first
// integration test to run pays the DROP SCHEMA + full embedded migration; every
// later test in the same process is a no-op here and resets via Reset. Any
// caller may pass any owner connection to the same database — the migration runs
// on whichever connection wins the race to the sync.Once, and the result is the
// same schema for all of them.
func EnsureSchema(ctx context.Context, owner *pgx.Conn) error {
	migrateOnce.Do(func() {
		if _, err := owner.Exec(ctx,
			`DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
			migrateErr = err
			return
		}
		core, err := migrations.Core()
		if err != nil {
			migrateErr = err
			return
		}
		custom, err := migrations.Custom()
		if err != nil {
			migrateErr = err
			return
		}
		if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
			migrateErr = err
			return
		}
		// The schema is migrated and provably empty exactly here, which is the
		// only moment a per-table "this is what zero rows costs" baseline can be
		// taken.
		sizes, err := tableSizes(ctx, owner)
		if err != nil {
			migrateErr = fmt.Errorf("recording empty-schema sizes: %w", err)
			return
		}
		emptySizes.Store(&sizes)
		// Last, and only on the success path: Pool refuses to hand out a
		// connection until this is set, so a pool can never predate the DROP
		// SCHEMA above.
		schemaReady.Store(true)
	})
	return migrateErr
}

// Reset empties every data table so the next test sees a clean database without
// re-running migrations.
//
// The reset is DELETE, not TRUNCATE, because TRUNCATE's cost is per-table, not
// per-row: it takes an ACCESS EXCLUSIVE lock and swaps a relfilenode for every
// table it touches, and CASCADE drags the whole FK graph in behind whichever
// table is named. Blindly truncating this schema costs ~500ms
// even when every table is already empty, which — at one reset per test — was
// most of the heaviest package's runtime. The DELETE batch measures ~15ms.
//
// Everything runs in ONE transaction, so the database is either fully reset or
// visibly failed. That matters more than it looks: the batch runs with FK
// enforcement off, so a half-applied reset would leave dangling references that
// Postgres will never complain about, and a later suite would read them as real
// data.
func Reset(ctx context.Context, owner *pgx.Conn) error {
	tx, err := owner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reset: %w", err)
	}
	if err := resetWithin(ctx, tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing reset: %w", err)
	}
	return nil
}

// resetWithin performs every step of the reset on one transaction.
func resetWithin(ctx context.Context, tx execQuerier) error {
	// Two transaction-local settings, both load-bearing, both reverted by the
	// commit or rollback that ends this transaction:
	//
	// session_replication_role = replica suppresses every non-ALWAYS trigger.
	// That covers the FK triggers, which is what lets one unordered DELETE batch
	// stand in for TRUNCATE ... CASCADE, AND the
	// append-only guards on audit_log and system_log, whose BEFORE DELETE
	// triggers RAISE unconditionally. TRUNCATE never fired row triggers, so
	// those guards are a dependency the DELETE introduces: every test writes an
	// audit row, so without replica mode the very first reset aborts. Do not
	// replace this with topologically ordered DELETEs.
	//
	// row_security = off closes the second exposure. TRUNCATE is not subject to
	// RLS; DELETE is, and every workspace_id table carries FORCE ROW LEVEL
	// SECURITY with deny-on-unset policies (gated by
	// migrations/schema_fitness_integration_test.go) while this transaction sets
	// no app.workspace_id GUC — so a role without BYPASSRLS would delete zero
	// rows and report a clean database. Setting session_replication_role is
	// itself SUSET, so it is the first statement such a role fails on and the
	// primary guard; this makes the RLS half fail loudly too rather than
	// filtering silently.
	if _, err := tx.Exec(ctx, `
		SELECT set_config('session_replication_role', 'replica', true),
		       set_config('row_security', 'off', true)`); err != nil {
		return fmt.Errorf("arming reset session: %w", err)
	}

	tables, err := queryIdents(ctx, tx,
		`SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname) `+resetTables+` ORDER BY n.nspname, c.relname`)
	if err != nil {
		return fmt.Errorf("listing data tables: %w", err)
	}
	// A migrated database always has tables, so an empty list means the schema is
	// gone, not that there is nothing to do. Reporting a clean reset for that is
	// the same silent-success shape the settings above exist to eliminate.
	if len(tables) == 0 {
		return fmt.Errorf("no data tables in public or ext — call EnsureSchema before Reset; the schema is missing or was dropped")
	}
	unbaselined, err := reclaimBloat(ctx, tx)
	if err != nil {
		return err
	}
	// Identifiers cannot be bound parameters, so the batch is built by
	// concatenation — but every name comes from quote_ident() over pg_class,
	// never from caller input, and is schema-qualified, so it is injection-safe
	// and search_path-independent.
	if _, err := tx.Exec(ctx, `DELETE FROM `+strings.Join(tables, `; DELETE FROM `)+`;`); err != nil {
		return fmt.Errorf("emptying data tables: %w", err)
	}
	if err := restartSequences(ctx, tx); err != nil {
		return err
	}
	if err := dropCustomFieldColumns(ctx, tx); err != nil {
		return err
	}
	// Every table is empty now, which is the only moment a table that did not
	// exist when EnsureSchema ran can be given a real baseline.
	return baselineNewTables(ctx, tx, unbaselined)
}

// baselineNewTables records the empty size of tables that appeared after
// EnsureSchema took its baseline — River migrates its own river_* schema the
// first time a suite boots a job client, partway through a package run. It runs
// at the end of a reset, where the tables are empty by construction, so the size
// it records is genuinely an empty size.
//
// Without this such a table would be measured against zero for the rest of the
// process, and the reclaim would TRUNCATE it on every single reset from the
// moment its own empty footprint exceeded the slack — the per-table cost this
// path exists to avoid, reintroduced for one table and invisible in a green lane.
func baselineNewTables(ctx context.Context, q execQuerier, unbaselined []string) error {
	if len(unbaselined) == 0 {
		return nil
	}
	sizes, err := tableSizes(ctx, q)
	if err != nil {
		return fmt.Errorf("baselining new tables: %w", err)
	}
	merged := make(map[string]int64, len(sizes))
	if recorded := emptySizes.Load(); recorded != nil {
		for name, size := range *recorded {
			merged[name] = size
		}
	}
	for _, name := range unbaselined {
		if size, ok := sizes[name]; ok {
			merged[name] = size
		}
	}
	emptySizes.Store(&merged)
	return nil
}

// reclaimBloat TRUNCATEs the tables that have grown well past their empty size.
// DELETE leaves dead tuples behind — in the indexes as well as the heap — so
// without this the volume the perf suite seeds (tens of thousands of rows) would
// keep costing every later test's scans until autovacuum caught up, and the
// customfields suites' ALTER TABLE would keep rewriting the dead weight with it.
//
// The trigger is GROWTH against the recorded empty baseline, not an absolute
// size, because on this schema an empty table is not small — index storage
// dominates a table that holds no rows. Any absolute threshold low enough to
// catch real bloat therefore sits close to the empty footprint, and the first
// migration to push a hot table over it would make every reset TRUNCATE that
// table forever, silently restoring the per-table cost this whole path exists to
// avoid. Measuring growth is immune to that: adding indexes raises the baseline
// with it.
//
// A table with no baseline yet is measured against zero for this one reset, and
// reports itself so the caller can baseline it once it is empty. Nothing here
// depends on how large an unbaselined table happens to be.
func reclaimBloat(ctx context.Context, tx execQuerier) ([]string, error) {
	sizes, err := tableSizes(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("measuring table sizes: %w", err)
	}
	var baseline map[string]int64
	if recorded := emptySizes.Load(); recorded != nil {
		baseline = *recorded
	}
	var bloated, unbaselined []string
	for name, size := range sizes {
		known, ok := baseline[name]
		if !ok {
			unbaselined = append(unbaselined, name)
		}
		if size > known+reclaimSlack {
			bloated = append(bloated, name)
		}
	}
	if len(bloated) == 0 {
		return unbaselined, nil
	}
	// Same injection posture as the DELETE batch: every name is quote_ident()
	// over pg_class and schema-qualified, never caller input.
	//
	// CASCADE because TRUNCATE still refuses a table whose referencing tables
	// are not named, structurally, whatever the triggers are doing. Everything
	// it reaches is being emptied in this transaction anyway.
	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(bloated, ", ")+` CASCADE`); err != nil {
		return nil, fmt.Errorf("reclaiming bloated tables: %w", err)
	}
	return unbaselined, nil
}

// tableSizes returns each reset-eligible table's total physical size — heap,
// indexes and TOAST — keyed by the same qualified identifier the reset emits.
// Size comes from pg_class.oid rather than a name cast to regclass: a
// name::regclass resolves through search_path, and the planner may evaluate that
// call before the schema filter, which makes it fail on the first
// information_schema row it sees.
func tableSizes(ctx context.Context, q execQuerier) (map[string]int64, error) {
	rows, err := q.Query(ctx,
		`SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname), pg_total_relation_size(c.oid) `+resetTables)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sizes := make(map[string]int64)
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil, err
		}
		sizes[name] = size
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sizes, nil
}

// restartSequences rewinds the counters the DELETE batch cannot: emptying rows
// leaves a sequence where it stood, and a test that asserts on a generated number
// must not see the previous test's value. Nearly every id in the schema is a
// client-side UUIDv7, so only a handful of sequences exist.
//
// Scoped to sequences OWNED BY a table the reset empties, which is what
// TRUNCATE ... RESTART IDENTITY covered. A sequence attached to anything the
// reset preserves, or attached to no table at all, is out of scope.
func restartSequences(ctx context.Context, tx execQuerier) error {
	seqs, err := queryIdents(ctx, tx, `
		SELECT quote_ident(n.nspname) || '.' || quote_ident(s.relname)
		FROM pg_class s
		JOIN pg_namespace n ON n.oid = s.relnamespace
		JOIN pg_depend d ON d.objid = s.oid AND d.classid = 'pg_class'::regclass
		                AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
		WHERE s.relkind = 'S'
		  AND d.refobjid IN (SELECT c.oid `+resetTables+`)`)
	if err != nil {
		return fmt.Errorf("listing sequences: %w", err)
	}
	if len(seqs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `ALTER SEQUENCE `+strings.Join(seqs, ` RESTART; ALTER SEQUENCE `)+` RESTART;`); err != nil {
		return fmt.Errorf("restarting sequences: %w", err)
	}
	return nil
}

// dropCustomFieldColumns reverts the runtime DDL the customfields engine adds —
// the cf_<slug> columns it appends to record tables (people, deals, …) as the
// system's single sanctioned ALTER-TABLE chokepoint. Emptying rows leaves the
// columns, so without this a cf_ column created by one test leaks into the next
// and is rejected as "taken platform-wide". No migrated baseline table carries a
// cf_-prefixed column, so every match here is a leaked custom field and safe to
// drop; DROP COLUMN cascades its generated cf_<slug>_check constraint with it.
func dropCustomFieldColumns(ctx context.Context, tx execQuerier) error {
	// Constrained to the tables the reset owns: information_schema.columns also
	// lists view columns, and a view over a record table exposes its cf_ columns.
	// ALTER TABLE cannot drop a view's column, and since the reset is one
	// transaction that failure would roll back the whole reset, not just itself.
	//
	// The join is on (schema, name), not on the bare name. Once the fragment spans
	// two schemas a name-only join means a public table is matched because an ext
	// table shares its name — and, worse, the converse: a relation the fragment
	// deliberately excludes would be readmitted through a same-named sibling in
	// the other schema. Row-wise membership keeps "the tables the reset owns"
	// meaning exactly what resetTables says.
	rows, err := tx.Query(ctx, `
		SELECT quote_ident(table_schema) || '.' || quote_ident(table_name), quote_ident(column_name)
		FROM information_schema.columns
		WHERE column_name LIKE 'cf\_%'
		  AND (table_schema, table_name) IN (SELECT n.nspname, c.relname `+resetTables+`)`)
	if err != nil {
		return fmt.Errorf("listing leaked custom-field columns: %w", err)
	}
	var stmts []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return fmt.Errorf("scanning leaked custom-field columns: %w", err)
		}
		// Same posture as the DELETE batch: quote_ident over a system catalog,
		// schema-qualified, never caller input.
		stmts = append(stmts, `ALTER TABLE `+table+` DROP COLUMN `+column+` CASCADE`)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing leaked custom-field columns: %w", err)
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("dropping leaked custom-field column: %w", err)
		}
	}
	return nil
}

// queryIdents runs a query whose single column is an already-quoted identifier
// and collects the results — the shape three of the reset's steps need.
func queryIdents(ctx context.Context, q execQuerier, sql string) ([]string, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var idents []string
	for rows.Next() {
		var ident string
		if err := rows.Scan(&ident); err != nil {
			return nil, err
		}
		idents = append(idents, ident)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return idents, nil
}
