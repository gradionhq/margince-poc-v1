// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

// The head probe decides whether EnsureSchema may take an already-migrated
// clone at its word instead of rebuilding it. Every way that decision can be
// wrong is a package whose tests run against a schema nobody chose, so each of
// the probe's proofs is asserted here in the shape that would actually slip
// past it — a stale template, an edited migration, a renumbered one, a clone
// from another branch, a populated one — rather than in the shape that is easy
// to plant.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

// atHead is the ledger this binary would record for a namespace, which is what
// every arm below starts from and then breaks in exactly one way.
func atHead(t *testing.T, ns dbmigrate.Namespace) map[string]recordedRow {
	t.Helper()
	recorded := make(map[string]recordedRow, len(ns.Migrations))
	for _, m := range ns.Migrations {
		recorded[m.Version] = recordedRow{name: m.Name, digest: dbmigrate.Digest(m)}
	}
	return recorded
}

func coreNamespace(t *testing.T) dbmigrate.Namespace {
	t.Helper()
	ns, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading the embedded core migrations: %v", err)
	}
	if len(ns.Migrations) == 0 {
		t.Fatal("the embedded core namespace is empty — every arm below would pass vacuously")
	}
	return ns
}

func TestAnAtHeadLedgerIsAccepted(t *testing.T) {
	ns := coreNamespace(t)
	if reason := namespaceAtHead(ns, atHead(t, ns)); reason != "" {
		t.Fatalf("a ledger recording exactly this binary's migrations was refused: %s", reason)
	}
}

// TestEveryWayALedgerCanLieIsRefused walks the five shapes a tracking table can
// take that all LOOK migrated. Each is a real failure mode: a template built
// before the newest migration landed; one whose migration was renumbered under
// it; one migrated by a binary that recorded no digest; one whose migration was
// edited after it was applied; and a clone taken from a template another branch
// built, which is at head for that branch and carries objects this one never
// created.
func TestEveryWayALedgerCanLieIsRefused(t *testing.T) {
	ns := coreNamespace(t)
	newest := ns.Migrations[len(ns.Migrations)-1]

	for _, tc := range []struct {
		name    string
		corrupt func(map[string]recordedRow)
		expects string
	}{
		{
			name:    "a migration this binary embeds was never applied",
			corrupt: func(l map[string]recordedRow) { delete(l, newest.Version) },
			expects: "behind this binary",
		},
		{
			name: "the version was applied under a different name",
			corrupt: func(l map[string]recordedRow) {
				row := l[newest.Version]
				row.name += "_other"
				l[newest.Version] = row
			},
			expects: "renumbered",
		},
		{
			name: "the row records no content digest",
			corrupt: func(l map[string]recordedRow) {
				row := l[newest.Version]
				row.digest = ""
				l[newest.Version] = row
			},
			expects: "no content digest",
		},
		{
			name: "the migration was edited after it was applied",
			corrupt: func(l map[string]recordedRow) {
				row := l[newest.Version]
				row.digest = strings.Repeat("0", len(row.digest))
				l[newest.Version] = row
			},
			expects: "different content",
		},
		{
			name: "the ledger records a migration this binary does not have",
			corrupt: func(l map[string]recordedRow) {
				l["9999999999"] = recordedRow{name: "from_another_branch", digest: "unchecked"}
			},
			expects: "different source tree",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := atHead(t, ns)
			tc.corrupt(ledger)
			reason := namespaceAtHead(ns, ledger)
			if reason == "" {
				t.Fatalf("the probe accepted a ledger that %s — EnsureSchema would have skipped the rebuild and every test in the package would run on that schema", tc.name)
			}
			if !strings.Contains(reason, tc.expects) {
				t.Errorf("the refusal does not say what is wrong: got %q, want it to mention %q", reason, tc.expects)
			}
		})
	}
}

// TestTheDigestSeparatesTheMigrationsParts pins the framing rather than the
// hash. Without length prefixes a version ending in digits and a name starting
// with them concatenate into the same bytes as the pair that splits them
// differently, and two different migrations would share one fingerprint.
func TestTheDigestSeparatesTheMigrationsParts(t *testing.T) {
	a := dbmigrate.Migration{Version: "0001", Name: "ab", UpSQL: "u", DownSQL: "d"}
	b := dbmigrate.Migration{Version: "0001a", Name: "b", UpSQL: "u", DownSQL: "d"}
	if dbmigrate.Digest(a) == dbmigrate.Digest(b) {
		t.Fatal("two migrations whose parts concatenate identically share a digest — the framing is not separating them")
	}
	// The ledger column holds this verbatim, so its shape is part of the
	// contract: 64 lower-case hex characters, one sha256.
	if got := dbmigrate.Digest(a); len(got) != 64 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("the digest is %q, which is not the 64 hex characters of a sha256", got)
	}
	rolledBackDifferently := a
	rolledBackDifferently.DownSQL = "DROP TABLE something_else"
	if dbmigrate.Digest(a) == dbmigrate.Digest(rolledBackDifferently) {
		t.Fatal("two migrations with different down-migrations share a digest — the suites in backend/migrations run that half")
	}
}

// TestUpStampsTheDigestOfWhatItApplied closes the loop the pure arms above
// cannot: that what dbmigrate.Up WRITES into a tracking table is what Digest
// computes for the migration it just applied.
//
// It migrates a scratch namespace through the real migrator, in this process,
// rather than reading the core ledger — that ledger was stamped by whichever
// binary last built the template, so a probe of it would keep passing over a
// migrator that had stopped stamping until somebody rebuilt.
func TestUpStampsTheDigestOfWhatItApplied(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	probe := dbmigrate.Migration{
		Version: "0001",
		Name:    "stamp_probe",
		UpSQL:   `SELECT 1`,
		DownSQL: `SELECT 1`,
	}
	ns := dbmigrate.Namespace{Name: "headprobe_stamp_probe", Migrations: []dbmigrate.Migration{probe}}
	table := "schema_migrations_" + ns.Name
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TABLE IF EXISTS `+table); err != nil {
			t.Errorf("dropping the scratch tracking table: %v", err)
		}
	})
	applied, err := dbmigrate.Up(ctx, owner, ns)
	if err != nil {
		t.Fatalf("applying the scratch namespace: %v", err)
	}
	if applied != 1 {
		t.Fatalf("the scratch namespace applied %d migrations, want 1 — nothing was stamped to read back", applied)
	}

	recorded, err := recordedMigrations(ctx, owner, table)
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	row, ok := recorded[probe.Version]
	if !ok {
		t.Fatalf("%s recorded no row for the migration that was just applied", table)
	}
	if row.digest == "" {
		t.Fatal("dbmigrate.Up recorded no content digest — every clone would then read as unverifiable and rebuild")
	}
	if want := dbmigrate.Digest(probe); row.digest != want {
		t.Fatalf("Up stamped %s where Digest computes %s — the probe compares two different things", row.digest, want)
	}
}

// TestThePreflightCountsTheLedgerColumnsByName proves preflightSQL asks what it
// claims to ask. A count that ignored its filter would return the same number
// for any column list, and the probe would then read an unverifiable ledger as
// a verified one.
func TestThePreflightCountsTheLedgerColumnsByName(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	var current string
	var columns int
	if err := owner.QueryRow(ctx, preflightSQL, ledgerColumns).Scan(&current, &columns); err != nil {
		t.Fatalf("running the preflight: %v", err)
	}
	if current == "" {
		t.Fatal("the preflight did not report which database this connection is on")
	}
	if want := 2 * len(ledgerColumns); columns != want {
		t.Fatalf("the preflight found %d ledger columns on a migrated database, want %d — the probe would refuse every clone", columns, want)
	}

	absent := append(append([]string{}, ledgerColumns...), "a_column_no_tracking_table_has")
	if err := owner.QueryRow(ctx, preflightSQL, absent).Scan(&current, &columns); err != nil {
		t.Fatalf("running the preflight with an absent column: %v", err)
	}
	if want := 2 * len(ledgerColumns); columns != want {
		t.Fatalf("asking for a column no tracking table has changed the count to %d, want %d — the preflight is not counting by name", columns, want)
	}
}

// TestAnUndeclaredDatabaseIsNeverReused holds the direction that keeps the skip
// safe. make test-integration-serial runs every package against the TEMPLATE
// itself, and tools/extmigrategate migrates a database it made mid-process:
// both reach EnsureSchema, neither may take the skip, and both must be silent
// about it because neither is a mistake.
func TestAnUndeclaredDatabaseIsNeverReused(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	for _, tc := range []struct {
		name    string
		declare string
	}{
		{name: "no lane declared a clone", declare: ""},
		{name: "the declared clone is a different database", declare: "margince_some_other_database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(CloneDBEnv, tc.declare)
			reusable, reason, err := reusableClone(ctx, owner)
			if err != nil {
				t.Fatalf("probing: %v", err)
			}
			if reusable {
				t.Fatal("the probe offered the skip for a database no lane declared — the serial lane would leave one package's residue in the template every clone is copied from")
			}
			if reason != "" {
				t.Errorf("refusing a database that was never declared reported %q; that refusal is about the caller, not about a template anybody has to fix", reason)
			}
		})
	}
}

// TestAPopulatedCloneIsNeverReused is the emptiness proof. EnsureSchema records
// emptySizes immediately after the probe agrees, and every later reset measures
// growth against that baseline — so a baseline taken over rows misreports the
// reclaim for the life of the process. Until #1994 the bench targets seeded a
// quarter of a million rows into the template every clone is copied from, which
// is the shape this refuses.
func TestAPopulatedCloneIsNeverReused(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, slug) VALUES (gen_random_uuid(), 'headprobe-populated')`); err != nil {
		t.Fatalf("seeding a row: %v", err)
	}
	t.Cleanup(func() {
		if err := Reset(context.Background(), owner); err != nil {
			t.Errorf("resetting after the populated-clone arm: %v", err)
		}
	})

	reason, err := schemaEmpty(ctx, owner)
	if err != nil {
		t.Fatalf("probing for rows: %v", err)
	}
	if reason == "" {
		t.Fatal("the probe called a database holding a workspace row empty — EnsureSchema would baseline every table size over those rows")
	}
	if !strings.Contains(reason, "public.workspace") {
		t.Errorf("the refusal does not name what it found: %q", reason)
	}

	// And through the whole probe, not only its emptiness step: a version that
	// proved the schema empty and then forgot to consult the answer would pass
	// every assertion above.
	if declared, ok := lookupCloneDB(); ok {
		t.Setenv(CloneDBEnv, declared)
		reusable, reason, err := reusableClone(ctx, owner)
		if err != nil {
			t.Fatalf("probing: %v", err)
		}
		if reusable {
			t.Fatal("reusableClone offered the skip for a populated clone — EnsureSchema would baseline every table size over those rows")
		}
		if !strings.Contains(reason, "public.workspace") {
			t.Errorf("the whole probe did not report what the emptiness step found: %q", reason)
		}
	}

	if err := Reset(ctx, owner); err != nil {
		t.Fatalf("resetting: %v", err)
	}
	if reason, err := schemaEmpty(ctx, owner); err != nil || reason != "" {
		t.Fatalf("a reset database was still called populated (err=%v): %s", err, reason)
	}
}

// TestASchemalessDatabaseIsNotMistakenForAnEmptyOne is the other half of the
// emptiness proof, and the half a "no rows anywhere" check gets wrong for free.
//
// "Every table this reset touches holds no rows" is trivially true of a database
// with NO TABLES AT ALL. A probe that stopped at the row count would call such a
// database empty, agree to reuse it, and hand EnsureSchema a connection to
// nothing — where the first suite to run would fail on a missing relation rather
// than on the schema that was never built. So the absence of tables is a
// REFUSAL, phrased as its own reason, not a pass.
//
// A throwaway database, because the point is a database with no schema and the
// clone every other test in this package shares must keep its own.
func TestASchemalessDatabaseIsNotMistakenForAnEmptyOne(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	const bare = "margince_headprobe_no_schema"
	if _, err := owner.Exec(ctx, `DROP DATABASE IF EXISTS `+bare); err != nil {
		t.Fatalf("clearing a previous run's throwaway database: %v", err)
	}
	if _, err := owner.Exec(ctx, `CREATE DATABASE `+bare); err != nil {
		t.Fatalf("creating the throwaway database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP DATABASE IF EXISTS `+bare); err != nil {
			t.Errorf("dropping the throwaway database: %v", err)
		}
	})

	cfg := owner.Config().Copy()
	cfg.Database = bare
	empty, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connecting to the throwaway database: %v", err)
	}
	defer func() {
		if err := empty.Close(context.Background()); err != nil {
			t.Errorf("closing the throwaway connection: %v", err)
		}
	}()

	reason, err := schemaEmpty(ctx, empty)
	if err != nil {
		t.Fatalf("probing a database with no schema: %v", err)
	}
	if reason == "" {
		t.Fatal("the probe called a database with no tables empty — reuse would hand every suite a connection to nothing, and the first missing relation would read as a product bug rather than as a schema that was never built")
	}
	if !strings.Contains(reason, "no data tables") {
		t.Errorf("the refusal does not say the schema is absent, which is what a reader has to act on: %q", reason)
	}
}

// TestTheLaneRunsOnTheCloneItDeclares is the wiring gate. The skip is silent
// when the declared name does not match, which is right for a suite that made
// its own database and wrong for a lane that exported the wrong one — that
// second case would turn the saving off across every package with nothing in
// any log to say so. This process is not one of those suites, so the two must
// agree here.
func TestTheLaneRunsOnTheCloneItDeclares(t *testing.T) {
	ctx := context.Background()
	owner := ownerConn(t)

	declared, ok := lookupCloneDB()
	if !ok {
		// The serial lane, by design — it runs on the template and declares
		// nothing. Assert that rather than skipping: a skip here reads exactly
		// like a pass.
		reusable, _, err := reusableClone(ctx, owner)
		if err != nil {
			t.Fatalf("probing: %v", err)
		}
		if reusable {
			t.Fatal("no clone was declared and the probe still offered the skip")
		}
		return
	}
	var current string
	if err := owner.QueryRow(ctx, `SELECT current_database()`).Scan(&current); err != nil {
		t.Fatalf("asking which database this is: %v", err)
	}
	if current != declared {
		t.Fatalf("the lane declared clone %q but this package is running on %q — every package would silently rebuild the schema it was just handed", declared, current)
	}
	reusable, reason, err := reusableClone(ctx, owner)
	if err != nil {
		t.Fatalf("probing: %v", err)
	}
	if !reusable {
		t.Fatalf("the lane's own clone was refused, so every package in this run pays a full schema rebuild it does not need: %s\n"+
			"A template migrated before content digests existed reads this way; `make test-db-up` rebuilds it.", reason)
	}
}
