// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

// CloneDBEnv is how a lane declares that this process's database is a
// throwaway clone it created from the migrated template for this package
// alone, and names it.
//
// The value is the DATABASE NAME, not a boolean, and the difference is the
// whole safety of the skip. Two callers inside the lane reach EnsureSchema
// with a database that is NOT the package's clone:
//
//   - make test-integration-serial runs every package against margince_test
//     ITSELF, the template each clone is copied from. Skipping the rebuild
//     there lets one package's residue persist into the template and get
//     file-copied into every later clone.
//   - tools/extmigrategate creates its own throwaway database mid-process and
//     migrates it through EnsureSchema, DEPENDING on the rebuild to strip the
//     lane clone's extension objects — its file comment states that as the
//     gate's premise.
//
// A boolean inherited from the lane's environment would have let the skip run
// in both. Comparing the name against current_database() refuses any database
// this process was not handed, and costs nothing.
//
// Unset means rebuild. That direction is not an accident either: a lane that
// forgets to declare its clone runs slower, while one that wrongly claims a
// shared database corrupts every package after it.
const CloneDBEnv = "MARGINCE_TEST_CLONE_DB"

// lookupCloneDB reads the lane's declaration. An empty value is treated as an
// absent one: a lane that expands an unset variable into the environment has
// declared nothing, and reading "" as a database name would compare it against
// current_database() and refuse in a second, less obvious place.
func lookupCloneDB() (string, bool) {
	declared, ok := os.LookupEnv(CloneDBEnv)
	if !ok || declared == "" {
		return "", false
	}
	return declared, true
}

// ledgerColumns is what the probe reads out of a tracking table, and the
// preflight counts these three by name.
//
// content_digest is in the list because a template migrated by a binary older
// than that column exists in the wild — an inner-loop template survives across
// a git pull (scripts/lib-testdb.sh ensure_template migrates it rather than
// rebuilding it). Reading it blind fails the whole probe with an undefined
// column instead of falling back, which turns a stale template from "slower"
// into "red".
var ledgerColumns = []string{"version", "name", "content_digest"}

// preflightSQL answers, in one round trip, the two questions that decide
// whether the ledger is even readable: which database this connection is on,
// and whether both tracking tables carry the three columns the probe reads.
//
// information_schema rather than to_regclass + a second lookup: one query, and
// a table missing entirely counts zero columns exactly as a table missing the
// digest does — both are "cannot verify", and the probe owes them the same
// answer.
const preflightSQL = `
	SELECT current_database(),
	       (SELECT count(*) FROM information_schema.columns
	         WHERE table_schema = 'public'
	           AND table_name IN ('schema_migrations_core', 'schema_migrations_custom')
	           AND column_name = ANY($1))`

// reusableClone reports whether owner's database may be taken as already
// migrated to this binary's head and still empty, so EnsureSchema can skip the
// drop and the full dbmigrate.Up.
//
// It returns a REASON alongside the verdict, and the caller prints it — but
// only for the refusals that say something is WRONG WITH THE TEMPLATE. The
// asymmetry matches scripts/lib-testdb.sh migrate_template, and the cut is
// between two different kinds of "no":
//
//   - This database is not the lane's clone (no variable, or a name that does
//     not match). That is a fact about the CALLER, not about any schema: the
//     serial lane and tools/extmigrategate both arrive here legitimately and
//     get exactly what they asked for. Silent.
//   - The lane's own clone is behind, diverged, unverifiable or already
//     populated. Somebody has to fix a template, and until they do the lane
//     pays the rebuild it was meant to stop paying. Reported.
//
// So a line in a lane log means a template to fix, and a lane that prints none
// is one where every package took the skip — which is what makes "no rebuild
// lines" a usable structural check on the whole run rather than a count of
// exceptions.
//
// An error is reserved for a database that could not be asked. Everything the
// probe can actually establish about the schema resolves to reuse-or-rebuild,
// because rebuilding is always correct.
//
// WHAT THIS PROVES, exactly, because the difference is the whole risk. It proves
// that the LEDGER records this binary's own migrations, by content, and that the
// tables a reset acts on hold no rows. It does NOT compare the live catalog
// against what those migrations build — a column somebody added to the template
// by hand is invisible here. That comparison cannot be made cheaply, and not for
// want of trying: the only way to know what a migration set builds is to build
// it, which is the rebuild being skipped. backend/migrations' own head-schema
// digest has the same shape and the same limit.
//
// What bounds it is the clone, not the probe. A reusable database is a file copy
// of margince_test taken for ONE package and dropped after it, and the lane
// rebuilds that template from scratch on every full run (scripts/lib-testdb.sh
// build_template); `make test-db-up` is the same thing by hand. So the window in
// which hand-made DDL can survive is the window in which somebody edits the
// template and then does not rebuild it — and the version, name and content
// checks below already refuse every way the migration SET can differ, which is
// how that template would ordinarily go stale.
func reusableClone(ctx context.Context, owner *pgx.Conn) (bool, string, error) {
	declared, ok := lookupCloneDB()
	if !ok {
		return false, "", nil
	}
	var current string
	var columns int
	if err := owner.QueryRow(ctx, preflightSQL, ledgerColumns).Scan(&current, &columns); err != nil {
		return false, "", fmt.Errorf("probing the test database for a reusable schema: %w", err)
	}
	if current != declared {
		// Silent: see above. A suite that made its own database mid-process is
		// the intended shape here, not a mistake. What a SILENT refusal would
		// otherwise hide — a lane that exported the wrong name, turning the skip
		// off across every package — is caught by its own gate in
		// headprobe_integration_test.go instead.
		return false, "", nil
	}
	if want := 2 * len(ledgerColumns); columns != want {
		return false, fmt.Sprintf("the migration ledger has %d of the %d columns the probe reads — this database was migrated by a binary that did not record content digests; `make test-db-up` rebuilds the template", columns, want), nil
	}
	if reason, err := ledgerAtHead(ctx, owner); err != nil || reason != "" {
		return false, reason, err
	}
	// A failed emptiness probe REFUSES rather than propagating, because this
	// function's contract is that everything it can establish about the schema
	// resolves to reuse-or-rebuild — rebuilding is always correct, and an error
	// here would instead abort every package in the process. The reachable cause
	// is the RLS refusal above: an owner without BYPASSRLS (which is what
	// scripts/deploy/db-bootstrap.sql creates outside the compose stack) cannot
	// read an ext_ table with row_security off. "Cannot prove empty" and "is not
	// empty" owe the caller the same answer.
	reason, err := schemaEmpty(ctx, owner)
	if err != nil {
		return false, fmt.Sprintf("the clone could not be proved empty, so it is rebuilt rather than trusted: %v", err), nil
	}
	if reason != "" {
		return false, reason, nil
	}
	return true, "", nil
}

// ledgerAtHead compares what each tracking table recorded against what this
// binary embeds, and returns the first disagreement in words.
func ledgerAtHead(ctx context.Context, owner *pgx.Conn) (string, error) {
	core, err := migrations.Core()
	if err != nil {
		return "", fmt.Errorf("loading the embedded core migrations: %w", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		return "", fmt.Errorf("loading the embedded custom migrations: %w", err)
	}
	for _, ns := range []dbmigrate.Namespace{core, custom} {
		recorded, err := recordedMigrations(ctx, owner, "schema_migrations_"+ns.Name)
		if err != nil {
			return "", err
		}
		if reason := namespaceAtHead(ns, recorded); reason != "" {
			return reason, nil
		}
	}
	return "", nil
}

// recordedRow is one tracking-table row as the probe reads it. digest is empty
// when the column is NULL, which is a row written before the digest existed.
type recordedRow struct {
	name   string
	digest string
}

func recordedMigrations(ctx context.Context, owner *pgx.Conn, table string) (map[string]recordedRow, error) {
	// The table name is this package's own constant prefix plus a namespace
	// from the embedded migrations, never caller input, and dbmigrate.Up
	// interpolates the identical string.
	rows, err := owner.Query(ctx, `SELECT version, name, coalesce(content_digest, '') FROM `+table)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", table, err)
	}
	defer rows.Close()

	recorded := map[string]recordedRow{}
	for rows.Next() {
		var version string
		var row recordedRow
		if err := rows.Scan(&version, &row.name, &row.digest); err != nil {
			return nil, fmt.Errorf("reading %s: %w", table, err)
		}
		recorded[version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", table, err)
	}
	return recorded, nil
}

// namespaceAtHead checks one namespace BOTH WAYS: every embedded migration is
// recorded under the same name with the same content, and nothing else is
// recorded at all.
//
// The second direction is not symmetry for its own sake. A clone taken from a
// template another branch built is at head for THAT branch — it carries schema
// objects this binary's migrations never created and its tests know nothing
// about, and every version this binary embeds is present, so a one-way check
// passes over it. Extra versions are the only evidence of that shape.
func namespaceAtHead(ns dbmigrate.Namespace, recorded map[string]recordedRow) string {
	for _, m := range ns.Migrations {
		row, ok := recorded[m.Version]
		switch {
		case !ok:
			return fmt.Sprintf("%s %s_%s is embedded but not recorded — this database is behind this binary", ns.Name, m.Version, m.Name)
		case row.name != m.Name:
			return fmt.Sprintf("%s %s was applied as %q but this binary carries %q there — the version was renumbered", ns.Name, m.Version, row.name, m.Name)
		case row.digest == "":
			// The refusal an upgrading operator ACTUALLY meets, and the reason
			// the column-count branch above is not: trackingTable's
			// ALTER TABLE ... ADD COLUMN IF NOT EXISTS runs on every migrate, and
			// scripts/lib-testdb.sh ensure_template migrates the existing template
			// before any package starts — so by the time this probe runs the
			// column is there and only its rows are old. It carries the remedy for
			// that reason.
			return fmt.Sprintf("%s %s_%s recorded no content digest, so what it applied cannot be compared with what this binary embeds — this template predates the digest and rows are never back-filled; `make test-db-up` rebuilds it", ns.Name, m.Version, m.Name)
		case row.digest != dbmigrate.Digest(m):
			return fmt.Sprintf("%s %s_%s was applied from different content than this binary embeds — the migration was edited after this database was migrated", ns.Name, m.Version, m.Name)
		}
	}
	if len(recorded) != len(ns.Migrations) {
		return fmt.Sprintf("%s records %d migrations where this binary embeds %d — this database was migrated from a different source tree", ns.Name, len(recorded), len(ns.Migrations))
	}
	return ""
}

// nonEmptyReportLimit is how many populated tables the refusal names before it
// summarises the rest. Enough to see the shape of the pollution, few enough
// that the line stays readable in a lane log.
const nonEmptyReportLimit = 5

// schemaEmpty proves that every table a reset acts on currently holds no rows.
//
// EnsureSchema records emptySizes right after this, and reclaimBloat measures
// every later reset against that baseline — so a baseline taken over populated
// tables makes the reclaim compare growth against rows that were already
// there, for the life of the process. "At head" does not imply "empty": until
// PR #1994 the bench targets seeded a quarter of a million rows into the
// template every clone is copied from, and the unconditional rebuild this skip
// removes is what laundered that.
//
// It asks the same resetTables fragment the reset itself acts on, so a table
// the reset would empty can never be one this probe does not look at.
func schemaEmpty(ctx context.Context, owner *pgx.Conn) (string, error) {
	tables, err := queryIdents(ctx, owner,
		`SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname) `+resetTables+` ORDER BY n.nspname, c.relname`)
	if err != nil {
		return "", fmt.Errorf("listing data tables to prove the clone is empty: %w", err)
	}
	// A migrated database always has tables, so an empty list means the schema
	// is not there — which is a rebuild, not an empty database.
	if len(tables) == 0 {
		return "no data tables in public or ext — this database carries a migration ledger but no schema", nil
	}
	// One statement, and it selects each table's INDEX rather than its name:
	// the names are already quote_ident() output from pg_class and would need
	// literal-quoting to travel back as values, while an integer needs no
	// quoting at all and cannot be anything but what this loop wrote.
	branches := make([]string, 0, len(tables))
	for i, table := range tables {
		branches = append(branches, `SELECT `+strconv.Itoa(i)+` WHERE EXISTS (SELECT 1 FROM `+table+`)`)
	}
	// In a transaction with row_security off, and the guard is NOT the set_config
	// — that call always succeeds, because row_security is a USERSET GUC. (The
	// SUSET one is session_replication_role, which is why resetWithin's first
	// statement fails loudly for a role that cannot set it. Do not carry that
	// reasoning across; these are different GUCs with different contexts.)
	//
	// What the setting buys is the ERROR. An ext_ table carries FORCE ROW LEVEL
	// SECURITY with a deny-on-unset policy and this connection sets no
	// app.workspace_id, so a role without BYPASSRLS reading it normally is shown
	// zero rows — and this probe would answer "empty" over a populated schema,
	// the one wrong answer it must never give, since EnsureSchema baselines every
	// table size on the strength of it. With row_security off that same role gets
	// SQLSTATE 42501 instead of a filtered result, which schemaEmpty turns into a
	// refusal below. Silence becomes a refusal; the transaction is what makes
	// that swap, so do not remove it as redundant.
	tx, err := owner.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("probing the clone for rows: %w", err)
	}
	// Read-only throughout, so the transaction is ended by rollback on every
	// path: there is nothing here to commit. On the caller's context, like
	// Reset's own rollback — a cancelled probe has no rollback worth waiting on.
	defer func() {
		//craft:ignore swallowed-errors a read-only probe transaction has nothing to lose on rollback, and the probe's own verdict is the error that matters
		_ = tx.Rollback(ctx)
	}()
	if _, err := tx.Exec(ctx, `SELECT set_config('row_security', 'off', true)`); err != nil {
		return "", fmt.Errorf("arming the emptiness probe: %w", err)
	}

	rows, err := tx.Query(ctx, strings.Join(branches, " UNION ALL "))
	if err != nil {
		return "", fmt.Errorf("probing the clone for rows: %w", err)
	}
	defer rows.Close()

	var populated []string
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return "", fmt.Errorf("probing the clone for rows: %w", err)
		}
		populated = append(populated, tables[i])
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("probing the clone for rows: %w", err)
	}
	if len(populated) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d table(s) already hold rows (%s) — the template this clone came from is not empty, and an empty-schema size baseline taken over them would misreport every later reset",
		len(populated), summarise(populated)), nil
}

// summarise names the first few entries and counts the rest.
func summarise(names []string) string {
	if len(names) <= nonEmptyReportLimit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:nonEmptyReportLimit], ", "), len(names)-nonEmptyReportLimit)
}
