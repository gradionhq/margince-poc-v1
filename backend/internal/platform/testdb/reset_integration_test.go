// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The reset is what every integration suite relies on for isolation, and every way
// it can fail is quiet: a table it stops emptying, an RLS-filtered DELETE that
// affects nothing, a cf_ column left behind. None of those announce themselves —
// they surface later as a flake in some unrelated suite that reads like a
// product bug. So the obligations are asserted here, derived from the catalog
// rather than from a list somebody has to remember to update.

func ownerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := EnsureSchema(context.Background(), conn); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return conn
}

// nonEmptyTables names every relation that could hold a test's rows and still
// does. It deliberately does NOT reuse resetTables — deriving the check from the
// same fragment the reset emits from would only confirm that the reset emptied
// what it chose to look at — but note what this can and cannot see: it only
// disagrees with the reset about a table that HOLDS ROWS, and a test can only
// seed a handful. Scope itself is asserted directly, over the whole catalog, by
// TestResetScopeCoversEveryDataRelation.
func nonEmptyTables(ctx context.Context, t *testing.T, owner *pgx.Conn) []string {
	t.Helper()
	tables, err := queryIdents(ctx, owner, `
		SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT LIKE 'pg\_%'
		  AND n.nspname <> 'information_schema'
		  AND c.relkind IN ('r', 'p', 'f')
		  AND c.relname NOT LIKE 'schema_migrations_%'
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	var dirty []string
	for _, table := range tables {
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if n > 0 {
			dirty = append(dirty, table)
		}
	}
	return dirty
}

// Every other test here proves the reset does the right thing to the tables it
// looks at. This one proves it looks at all of them, which no row-seeding test
// can: narrowing resetTables — one stray predicate, or the relkind filter
// missing a class the schema later gains — makes the reset silently stop
// emptying whole table families, and a test only notices for a table it happens
// to have seeded. Comparing the reset's own scope against the catalog closes
// that whole axis in one assertion rather than one seed per table.
func TestResetScopeCoversEveryDataRelation(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	inScope, err := queryIdents(ctx, owner,
		`SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname) `+resetTables+` ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("listing the reset's scope: %v", err)
	}
	// Derived independently and deliberately wider: any relation in a schema the
	// migration lane owns that stores rows, whatever its relkind, minus the ledger
	// the reset preserves on purpose. Partitioned parents ('p') count — their
	// leaves are emptied, but a parent outside the reset's scope also hides from
	// reclaimBloat and restartSequences.
	expected, err := queryIdents(ctx, owner, `
		SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('public', 'ext')
		  AND c.relkind IN ('r', 'p')
		  AND c.relname NOT LIKE 'schema_migrations_%'
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		t.Fatalf("listing every data relation: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("found no data relations — this test would pass vacuously")
	}

	scoped := make(map[string]bool, len(inScope))
	for _, name := range inScope {
		scoped[name] = true
	}
	var missed []string
	for _, name := range expected {
		if !scoped[name] {
			missed = append(missed, name)
		}
	}
	if len(missed) > 0 {
		t.Errorf("the reset does not cover %d data relation(s): %v — rows written there survive every reset "+
			"and leak into the next test, which no row-count assertion here would notice", len(missed), missed)
	}
}

func TestResetEmptiesEveryDataTableIncludingTheAppendOnlyOnes(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("reset to a known-clean start: %v", err)
	}

	ws := "00000000-0000-7000-8000-0000000000aa"
	if _, err := owner.Exec(ctx, `
		INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'reset', 'reset-probe', 'EUR')`, ws); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	// audit_log carries a BEFORE DELETE trigger that raises unconditionally, so
	// this row is the one that proves the reset suppresses the append-only guards
	// and not merely the FK triggers.
	if _, err := owner.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, 'human', 'human:probe', 'create', 'workspace', $1)`, ws); err != nil {
		t.Fatalf("seeding audit_log: %v", err)
	}
	// TWO rows, so the sequence lands on a value the restart must visibly move.
	// One row would leave last_value = 1, which is also where RESTART leaves it,
	// and the assertion below could then never fail.
	if _, err := owner.Exec(ctx, `
		INSERT INTO event_outbox (stream, envelope)
		VALUES ('probe', '{}'::jsonb), ('probe', '{}'::jsonb)`); err != nil {
		t.Fatalf("seeding event_outbox: %v", err)
	}

	if dirty := nonEmptyTables(ctx, t, owner); len(dirty) == 0 {
		t.Fatal("seeding wrote nothing — this test would pass vacuously")
	}

	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if dirty := nonEmptyTables(ctx, t, owner); len(dirty) > 0 {
		t.Errorf("Reset left rows in %v — every reset-eligible table must be empty", dirty)
	}

	// last_value alone is ambiguous — it reads 1 both for "one row consumed" and
	// for "restarted" — so is_called is what actually distinguishes a rewound
	// sequence from a used one.
	var seqLast int64
	var seqCalled bool
	if err := owner.QueryRow(ctx,
		`SELECT last_value, is_called FROM event_outbox_seq_seq`).Scan(&seqLast, &seqCalled); err != nil {
		t.Fatalf("reading sequence: %v", err)
	}
	if seqLast != 1 || seqCalled {
		t.Errorf("event_outbox_seq_seq is at (last_value=%d, is_called=%t), want (1, false) — "+
			"Reset must rewind sequences, since emptying rows does not", seqLast, seqCalled)
	}
}

func TestResetPreservesTheMigrationLedger(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	var before int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM schema_migrations_core`).Scan(&before); err != nil {
		t.Fatalf("counting the core ledger: %v", err)
	}
	if before == 0 {
		t.Fatal("the core migration ledger is empty — EnsureSchema did not record anything to preserve")
	}
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var after int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM schema_migrations_core`).Scan(&after); err != nil {
		t.Fatalf("counting the core ledger: %v", err)
	}
	if after != before {
		t.Errorf("Reset changed schema_migrations_core from %d to %d rows — a later process must still see a migrated database", before, after)
	}
}

func TestResetDropsLeakedCustomFieldColumns(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)
	if _, err := owner.Exec(ctx, `ALTER TABLE person ADD COLUMN cf_reset_probe text`); err != nil {
		t.Fatalf("adding a cf_ column: %v", err)
	}
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var leaked int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE 'cf\_%'`).Scan(&leaked); err != nil {
		t.Fatalf("counting cf_ columns: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d cf_ column(s) survived Reset — the next test would read them as taken platform-wide", leaked)
	}
}

// DELETE alone would leave a bulk-seeded table's dead tuples in place, and the
// whole lane pays for that afterwards: the perf suite seeds tens of thousands of
// rows, and every later test's scans and ALTER TABLEs would drag the corpse
// along until autovacuum caught up. Reset is supposed to hand the storage back.
// Without the reclaim path this fails — the rows go, the pages do not.
func TestResetReclaimsStorageFromABulkSeededTable(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("reset to a known-clean start: %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO workspace (id, name, slug, base_currency)
		SELECT uuidv7(), 'bulk' || g, 'bulk-' || g, 'EUR' FROM generate_series(1, 20000) g`); err != nil {
		t.Fatalf("bulk-seeding workspace: %v", err)
	}

	const table = `public.workspace`
	baseline := *emptySizes.Load()
	grown, err := tableSizes(ctx, owner)
	if err != nil {
		t.Fatalf("measuring table sizes: %v", err)
	}
	if grown[table] <= baseline[table]+reclaimSlack {
		t.Fatalf("workspace only reached %d bytes against a %d-byte baseline (slack %d) — "+
			"the seed is too small to exercise the reclaim path, so this test would pass vacuously",
			grown[table], baseline[table], reclaimSlack)
	}

	var ledger int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM schema_migrations_core`).Scan(&ledger); err != nil {
		t.Fatalf("counting the core ledger: %v", err)
	}

	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	after, err := tableSizes(ctx, owner)
	if err != nil {
		t.Fatalf("measuring table sizes: %v", err)
	}
	if after[table] > baseline[table]+reclaimSlack {
		t.Errorf("workspace still holds %d bytes after Reset (baseline %d, slack %d) — "+
			"the storage was not reclaimed, so every later test pays for this bloat",
			after[table], baseline[table], reclaimSlack)
	}
	// The reclaim path TRUNCATEs ... CASCADE, which is the one statement in the
	// reset that could reach the preserved ledger through a foreign key.
	var ledgerAfter int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM schema_migrations_core`).Scan(&ledgerAfter); err != nil {
		t.Fatalf("counting the core ledger: %v", err)
	}
	if ledgerAfter != ledger {
		t.Errorf("the reclaim path changed schema_migrations_core from %d to %d rows — "+
			"TRUNCATE ... CASCADE must not reach the preserved ledger", ledger, ledgerAfter)
	}
}

// A reset that pays TRUNCATE's per-table price costs ~500ms per test even when
// every table is already empty, which is enough to put the heaviest package over
// the lane's per-package cap. The sharded CI lane cannot see that — each shard
// runs a fraction of every package, so no shard approaches the cap — so the
// property is asserted here instead.
//
// Rewriting a relation is what makes TRUNCATE expensive and exactly what DELETE
// does not do, so relfilenode identity separates the two with no timing
// assertion and no flakiness: on an ALREADY-EMPTY schema a reset must move no
// storage at all. A blanket TRUNCATE fails this immediately, and so does a
// reclaim predicate that has drifted into firing on empty tables — one
// TRUNCATE ... CASCADE drags in ~110 tables behind whichever one it names.
func TestResetOnAnEmptySchemaRewritesNoStorage(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("reset to a known-empty schema: %v", err)
	}

	before, err := relfilenodes(ctx, owner)
	if err != nil {
		t.Fatalf("reading relfilenodes: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("read no relfilenodes — this test would pass vacuously")
	}

	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	after, err := relfilenodes(ctx, owner)
	if err != nil {
		t.Fatalf("reading relfilenodes: %v", err)
	}
	var rewritten, vanished []string
	for name, node := range before {
		switch after[name] {
		case node:
		case 0:
			vanished = append(vanished, name)
		default:
			rewritten = append(rewritten, name)
		}
	}
	if len(vanished) > 0 {
		sort.Strings(vanished)
		t.Errorf("relation(s) %v disappeared across the reset — a reset must not drop tables", vanished)
	}
	if len(rewritten) > 0 {
		sort.Strings(rewritten)
		shown := rewritten
		suffix := ""
		if len(shown) > 3 {
			shown, suffix = shown[:3], ", …"
		}
		t.Errorf("Reset rewrote %d of %d relations on an already-empty schema (%v%s) — "+
			"the reset must not pay TRUNCATE's per-table cost when there is nothing to reclaim",
			len(rewritten), len(before), shown, suffix)
	}
}

// relfilenodes maps each reset-eligible table to its on-disk relation. TRUNCATE
// swaps it; DELETE leaves it alone.
func relfilenodes(ctx context.Context, owner *pgx.Conn) (map[string]uint32, error) {
	rows, err := owner.Query(ctx,
		`SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname), c.relfilenode `+resetTables)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make(map[string]uint32)
	for rows.Next() {
		var name string
		var node uint32
		if err := rows.Scan(&name, &node); err != nil {
			return nil, err
		}
		nodes[name] = node
	}
	return nodes, rows.Err()
}

// Whether the baseline was taken on an empty schema is a property of where
// EnsureSchema records it, not of its values, so this cannot assert it directly.
// What it can assert is that a baseline exists at all and that every entry is
// sane — present, non-zero, and below the slack, which is what keeps the
// reclaim from firing on a table that merely holds no rows.
func TestEnsureSchemaRecordsASaneBaseline(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t) // runs EnsureSchema; the baseline is its side effect

	if recorded := emptySizes.Load(); recorded == nil || len(*recorded) == 0 {
		t.Fatal("EnsureSchema recorded no empty-schema baseline — reclaimBloat would silently fall back to an absolute size threshold")
	}

	// The slack half is checked against a freshly measured empty schema rather
	// than against the stored baseline. Both would read the same numbers today,
	// but the stored map is process-global and baselineNewTables may replace an
	// entry mid-run, which would make a failure here point at EnsureSchema for
	// something it did not do. Measuring live keeps the message true, and covers
	// tables that appeared after EnsureSchema as well as the ones it recorded.
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("reset to an empty schema: %v", err)
	}
	sizes, err := tableSizes(ctx, owner)
	if err != nil {
		t.Fatalf("measuring table sizes: %v", err)
	}
	if len(sizes) == 0 {
		t.Fatal("measured no tables — this test would pass vacuously")
	}
	for name, size := range sizes {
		if size <= 0 {
			t.Errorf("%s occupies %d bytes — a migrated table always holds storage, so this measurement is not of the migrated schema", name, size)
		}
		// reclaimBloat measures a table with no baseline against zero, which is
		// only safe while no empty table reaches the slack on its own. That holds
		// today with room to spare, and it is the kind of margin a few added
		// indexes erode silently: once an empty table crosses the slack, the
		// fallback TRUNCATEs it on every reset and the per-test cost this whole
		// path avoids comes straight back.
		if size >= reclaimSlack {
			t.Errorf("%s occupies %d bytes empty, at or past the %d-byte slack — a table with no "+
				"baseline would now be TRUNCATEd on every reset; raise reclaimSlack",
				name, size, reclaimSlack)
		}
	}
}

// TestResetEmptiesAnExtensionTable is the ext half of the reset's scope, and it
// has to seed its own table because the tree ships no extension with migrations
// yet: 0200 creates the ext SCHEMA, and a unit's tables arrive with the unit.
//
// That absence is exactly the hazard. While ext is empty every assertion above
// passes whether or not the reset looks there, so the omission would be found by
// the first suite whose extension rows survived into a later test — as a flake
// somewhere else. The table is created here, emptied by the reset under test,
// and dropped again, so the claim is checked today against the shape a unit will
// actually ship (ext.ext_<unit>_<table>) rather than deferred to the unit.
func TestResetEmptiesAnExtensionTable(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)
	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("reset to a known-clean start: %v", err)
	}
	// Dropped rather than left behind: this process's later tests measure the
	// catalog, and a table only one test knows about would read as a relation the
	// reset must account for.
	if _, err := owner.Exec(ctx, `
		CREATE TABLE ext.ext_resetprobe_note (id uuid PRIMARY KEY, body text NOT NULL)`); err != nil {
		t.Fatalf("creating the probe table in ext: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TABLE IF EXISTS ext.ext_resetprobe_note`); err != nil {
			t.Errorf("dropping the probe table: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `
		INSERT INTO ext.ext_resetprobe_note (id, body)
		VALUES ('00000000-0000-7000-8000-0000000000bb', 'a row an extension wrote')`); err != nil {
		t.Fatalf("seeding the probe table: %v", err)
	}

	var seeded int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM ext.ext_resetprobe_note`).Scan(&seeded); err != nil {
		t.Fatalf("counting the seeded rows: %v", err)
	}
	if seeded != 1 {
		t.Fatalf("the probe table holds %d rows before the reset — this test would pass vacuously", seeded)
	}

	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var remaining int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM ext.ext_resetprobe_note`).Scan(&remaining); err != nil {
		t.Fatalf("counting the surviving rows: %v", err)
	}
	if remaining != 0 {
		t.Errorf("Reset left %d row(s) in ext.ext_resetprobe_note — an extension's rows would bleed into every later test in this process", remaining)
	}
}
