// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command extmigrategate applies one extension unit's migrations as its
// restricted ext_<name> role against a throwaway database and then asserts
// what the catalog actually holds (ADR-0069).
//
// It is the closing gate of the extension migration rules, and the only one
// that is not textual. gen-composition's pre-apply rule reads the SQL and
// therefore can only refuse the shapes a scanner can see; this one refuses
// what PostgreSQL ended up with. The two halves of that are deliberate:
//
//   - APPLYING AS THE ROLE turns most of the rules into refusals the database
//     issues for free. The role holds CREATE, USAGE on ext and nothing on
//     public, so a migration that writes a core relation, creates a table
//     outside ext, or reaches an unqualified name through search_path fails at
//     the statement rather than being detected afterwards. cmd/migrate opens a
//     single owner connection with no SET ROLE, and in dev and CI that owner is
//     a superuser — exactly the environment this gate runs in — so applying as
//     the owner and inspecting the result afterwards would be a strictly weaker
//     gate that also has to enumerate every way to escape.
//   - ASSERTING POSITIVELY, from an allowlist, is what makes the remainder
//     complete. A denylist of known-bad shapes is only ever as good as its
//     author's imagination; "these relkinds, these columns, this one policy,
//     these grants, nothing else" refuses the shapes nobody thought of.
//
// Usage:
//
//	extmigrategate -unit crm-demo -dir extensions/crm-demo/migrations -dsn <throwaway>
//
// The DSN must name a THROWAWAY database, migrated to head (the gate needs
// public.workspace and the ext schema) whose owner may create roles. The gate
// creates the ext_<name> role, applies, asserts, reverts, asserts the revert,
// and drops the role again; it never leaves the database or the cluster
// changed on a clean run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func main() {
	unit := flag.String("unit", "", "extension unit name, e.g. crm-demo")
	dir := flag.String("dir", "", "the unit's migrations directory")
	dsn := flag.String("dsn", "", "DSN of a throwaway, migrated database whose owner may create roles")
	flag.Parse()

	if err := run(context.Background(), *unit, *dir, *dsn); err != nil {
		// stderr and a non-zero status, with the offending object in the
		// message: this runs as a `make check` step, and the only useful
		// output of a failing gate is which object broke which rule.
		fmt.Fprintf(os.Stderr, "extmigrategate: %v\n", err)
		os.Exit(1)
	}
}

// run is the whole gate, separated from main so the integration suite drives
// it in-process and reads the error rather than parsing a subprocess's stderr.
func run(ctx context.Context, unit, dir, dsn string) (err error) {
	switch {
	case unit == "":
		return errors.New("-unit is required")
	case dir == "":
		return errors.New("-dir is required")
	case dsn == "":
		return errors.New("-dsn is required")
	}

	// NamespaceFor, not a local derivation: the table prefix, the role name and
	// the migration namespace are ONE namespace, and a second spelling of the
	// mapping is how they start disagreeing.
	namespace, err := dbmigrate.NamespaceFor(unit)
	if err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}

	// Load enforces the .up/.down pairing, so a unit whose migration cannot be
	// reverted is refused before anything touches the database — the gate
	// asserts the revert, and an unrevertable migration has no revert to assert.
	// Rooted at the PARENT with the directory as the sub-path: dbmigrate.Load
	// joins dir+"/"+name, and io/fs rejects the "./name" that a "." dir would
	// produce as an invalid path.
	dir = filepath.Clean(dir)
	migrations, err := dbmigrate.Load(os.DirFS(filepath.Dir(dir)), filepath.Base(dir))
	if err != nil {
		return fmt.Errorf("%s: %s: %w", unit, dir, err)
	}
	if len(migrations) == 0 {
		return fmt.Errorf("%s: %s holds no NNNN_name.up.sql/.down.sql pair — a unit with a migrations directory declares at least one", unit, dir)
	}

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("%s: connecting to the throwaway database: %w", unit, err)
	}
	defer closeQuietly(ctx, admin)

	// The success line is deferred, and registered BEFORE the cleanup defer so
	// that it runs AFTER it: cleanup can turn a passing run into a failing one,
	// and a gate that printed OK and then exited 1 would have told the operator
	// both things.
	var summary string
	defer func() {
		if err == nil && summary != "" {
			fmt.Println(summary)
		}
	}()

	role, err := mintRole(ctx, admin, namespace, dsn)
	if err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	// Ordered: the role's connection must be gone before the role can be
	// dropped, and the role must be dropped even when an assertion fails —
	// a login role left on the cluster owns the tables it created and can
	// drop their tenant-isolation policies.
	//
	// A failed cleanup is a FAILED GATE, not a note on stderr. The role is a
	// LOGIN role that owns whatever the migrations just created, so leaving one
	// behind is a standing credential on the cluster; a run that printed OK
	// while leaking one would be reporting the opposite of what happened. It
	// joins rather than replaces an assertion failure, because which rule broke
	// is still the more useful half of the message.
	defer func() {
		closeQuietly(ctx, role.conn)
		dropErr := role.drop(ctx, admin)
		switch {
		case dropErr == nil:
		case err == nil:
			err = fmt.Errorf("%s: %w", unit, dropErr)
		default:
			err = fmt.Errorf("%w (and cleaning up afterwards: %s: %w)", err, unit, dropErr)
		}
	}()

	if err := applyUp(ctx, role.conn, migrations); err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	if err := validateCatalog(ctx, role.conn, namespace); err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	// The revert is validated too: a unit whose down-migration leaves an object
	// behind leaves an ext_<name>-owned table on a database that no longer
	// records the migration, which the next apply then fails on for a reason
	// that names the wrong migration.
	if err := applyDown(ctx, role.conn, migrations); err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	if err := validateReverted(ctx, role.conn, namespace, unit); err != nil {
		return err
	}
	// Composed here rather than in main so the namespace is the one that was
	// actually used, not a second derivation of it.
	summary = fmt.Sprintf("OK: extmigrategate %s — %d migration(s) apply as %s, the catalog holds, and the revert is clean",
		unit, len(migrations), namespace)
	return nil
}

// applyUp runs each migration's up half in its own transaction, oldest first.
//
// dbmigrate.Up is deliberately NOT used: it records into a tracking table it
// creates with an unqualified CREATE TABLE, which resolves through search_path
// to public — a schema this role has no CREATE on, by design. There is nothing
// for a tracking table to track here anyway, because the database is thrown
// away after one run and always starts empty.
func applyUp(ctx context.Context, conn *pgx.Conn, migrations []dbmigrate.Migration) error {
	for _, m := range migrations {
		if err := inTx(ctx, conn, m.UpSQL); err != nil {
			return fmt.Errorf("applying %s_%s.up.sql as the extension role: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// applyDown reverts newest first, mirroring dbmigrate.Down's order.
func applyDown(ctx context.Context, conn *pgx.Conn, migrations []dbmigrate.Migration) error {
	for i := len(migrations) - 1; i >= 0; i-- {
		m := migrations[i]
		if err := inTx(ctx, conn, m.DownSQL); err != nil {
			return fmt.Errorf("reverting %s_%s.down.sql as the extension role: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// inTx runs one migration's SQL atomically, so a failure leaves the throwaway
// database at the last good version and the error names the migration that
// broke rather than a later one confused by half-applied state.
func inTx(ctx context.Context, conn *pgx.Conn, sql string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		// The migration failure is what the author must see; rolling back a
		// transaction that is being abandoned either way is cleanup.
		//craft:ignore swallowed-errors the migration error supersedes a rollback failure on this abandoned tx
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// closeQuietly closes a connection on a path that is already returning its own
// error, or exiting. A close failure here cannot change the verdict and would
// only displace the message that can.
func closeQuietly(ctx context.Context, conn *pgx.Conn) {
	if conn == nil {
		return
	}
	//craft:ignore swallowed-errors a close failure cannot change a verdict already decided
	_ = conn.Close(ctx)
}
