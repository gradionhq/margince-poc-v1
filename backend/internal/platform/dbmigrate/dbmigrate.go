// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package dbmigrate applies the repo's SQL migrations. It exists instead
// of golang-migrate because the schema has THREE ownership namespaces
// (ADR-0017: core/, custom/, per-jurisdiction packs), each with its own
// tracking table and a fixed core-then-custom apply order — a shape that
// would need one golang-migrate instance per namespace anyway.
//
// A version is ordered as a string, never parsed as a number, so a
// namespace may hold more than one version shape as long as the shapes
// sort into one another correctly. Which shapes a namespace uses is the
// namespace's own business, not this package's.
package dbmigrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// suffixUp / suffixDown name the two halves of a reversible migration pair.
const (
	suffixUp   = ".up.sql"
	suffixDown = ".down.sql"
)

// Migration is one reversible schema step: <version>_name.up.sql + .down.sql.
type Migration struct {
	Version string // "1787000000" (core, unix seconds), "0001" (core, the closed baseline) or "20260620143000" (custom)
	Name    string
	UpSQL   string
	DownSQL string
}

// Namespace is one migration ownership domain with its own tracking table.
type Namespace struct {
	// Name keys the tracking table: schema_migrations_<name>.
	Name       string
	Migrations []Migration
}

// Digest is the content fingerprint of one migration, recorded alongside its
// tracking row so a later reader can tell "this database applied version X"
// from "this database applied the migration this binary calls X".
//
// The ledger's version and name cannot answer that. assertLedgerMatches below
// catches a RENUMBER because the name moved; nothing catches an EDIT, and
// scripts/lib-testdb.sh says so outright about the level above ("records a
// version, not a checksum"). Every consumer that wants to trust an
// already-migrated database — testdb's head probe is the first — needs the
// content, so it is stamped once, here, rather than derived differently by
// each of them.
//
// Both halves of the pair are covered, not just UpSQL. A down-migration is
// executable content this binary would run against a database it decided was
// current, and the suites in backend/migrations do run it; a digest that
// ignored it would call two binaries identical while their rollbacks differ.
//
// The parts are length-prefixed so no concatenation of one migration can be
// read as another: without it a version ending in a digit and a name starting
// with one would hash the same as the pair that splits them differently.
//
// Up STAMPS this and judges nothing. Making the production migrate path REFUSE
// a version whose recorded digest no longer matches — the way assertLedgerMatches
// already refuses a renamed one — decides how a live installation fails at boot,
// which is a product call rather than a test-lane one: #2141 carries it, with
// Down (running the wrong rollback) as the sharper half.
func Digest(m Migration) string {
	var framed strings.Builder
	for _, part := range []string{m.Version, m.Name, m.UpSQL, m.DownSQL} {
		framed.WriteString(strconv.Itoa(len(part)))
		framed.WriteString(":")
		framed.WriteString(part)
	}
	sum := sha256.Sum256([]byte(framed.String()))
	return hex.EncodeToString(sum[:])
}

// advisoryLockKey serializes concurrent migrators cluster-wide; the value
// is arbitrary but must never change.
const advisoryLockKey = 74_726_531 // "margince migrate"

// Load reads <version>_name.up.sql / <version>_name.down.sql pairs from dir. A
// missing .down.sql is an error: every migration must reverse (B-EP02.1b).
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("pgmigrate: reading %s: %w", dir, err)
	}

	byKey := map[string]*Migration{}
	for _, e := range entries {
		name := e.Name()
		var suffix string
		switch {
		case strings.HasSuffix(name, suffixUp):
			suffix = suffixUp
		case strings.HasSuffix(name, suffixDown):
			suffix = suffixDown
		default:
			continue
		}

		key := strings.TrimSuffix(name, suffix)
		version, title, ok := strings.Cut(key, "_")
		if !ok {
			return nil, fmt.Errorf("pgmigrate: %s: want <version>_<name>%s", name, suffix)
		}

		sql, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("pgmigrate: reading %s: %w", name, err)
		}

		m := byKey[key]
		if m == nil {
			m = &Migration{Version: version, Name: title}
			byKey[key] = m
		}
		if suffix == suffixUp {
			m.UpSQL = string(sql)
		} else {
			m.DownSQL = string(sql)
		}
	}

	migrations := make([]Migration, 0, len(byKey))
	for _, m := range byKey {
		if m.UpSQL == "" || m.DownSQL == "" {
			return nil, fmt.Errorf("pgmigrate: %s_%s: every migration needs both .up.sql and .down.sql", m.Version, m.Name)
		}
		migrations = append(migrations, *m)
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })

	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version == migrations[i-1].Version {
			return nil, fmt.Errorf("pgmigrate: duplicate version %s", migrations[i].Version)
		}
	}
	return migrations, nil
}

// Up applies every pending migration in each namespace, in the order the
// namespaces are given (core before custom before packs — ADR-0017).
// Each migration runs in its own transaction together with its tracking
// row, so a failure leaves the database at the last good version, never
// half-applied. Idempotent: a second run is a no-op.
func Up(ctx context.Context, conn *pgx.Conn, namespaces ...Namespace) (applied int, err error) {
	if err := lock(ctx, conn); err != nil {
		return 0, err
	}
	defer unlock(ctx, conn)

	for _, ns := range namespaces {
		table, err := trackingTable(ctx, conn, ns.Name)
		if err != nil {
			return applied, err
		}
		done, err := appliedVersions(ctx, conn, table)
		if err != nil {
			return applied, err
		}

		for _, m := range ns.Migrations {
			if err := assertLedgerMatches(ns.Name, done, m); err != nil {
				return applied, err
			}
			if _, isDone := done[m.Version]; isDone {
				continue
			}
			if err := inTx(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
					return err
				}
				_, err := tx.Exec(ctx,
					fmt.Sprintf(`INSERT INTO %s (version, name, content_digest) VALUES ($1, $2, $3)`, table),
					m.Version, m.Name, Digest(m))
				return err
			}); err != nil {
				return applied, fmt.Errorf("pgmigrate: %s %s_%s: %w", ns.Name, m.Version, m.Name, err)
			}
			applied++
		}
	}
	return applied, nil
}

// Down reverts up to n applied migrations of ONE namespace, newest first.
// Reverting across namespaces is deliberate manual work, not one command.
func Down(ctx context.Context, conn *pgx.Conn, ns Namespace, n int) (reverted int, err error) {
	if err := lock(ctx, conn); err != nil {
		return 0, err
	}
	defer unlock(ctx, conn)

	table, err := trackingTable(ctx, conn, ns.Name)
	if err != nil {
		return 0, err
	}
	done, err := appliedVersions(ctx, conn, table)
	if err != nil {
		return 0, err
	}

	for i := len(ns.Migrations) - 1; i >= 0 && reverted < n; i-- {
		m := ns.Migrations[i]
		// Checked on the way down too, and for a sharper reason: reverting a
		// version whose ledger row names a different migration would run THIS
		// migration's down against a schema the other one built, then delete
		// the row that was the only record either had been applied.
		if err := assertLedgerMatches(ns.Name, done, m); err != nil {
			return reverted, err
		}
		if _, isDone := done[m.Version]; !isDone {
			continue
		}
		if err := inTx(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.DownSQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE version = $1`, table), m.Version)
			return err
		}); err != nil {
			return reverted, fmt.Errorf("pgmigrate: %s revert %s_%s: %w", ns.Name, m.Version, m.Name, err)
		}
		reverted++
	}
	return reverted, nil
}

// NamespaceFor maps an extension unit name onto its migration namespace:
// `foo-1` → `ext_foo_1`, tracked in `schema_migrations_ext_foo_1`. An
// extension's migrations are a fourth ownership domain alongside the three in
// this package's doc comment, and this is the only place the unit-name →
// namespace mapping is spelled for them.
//
// It derives through extension.Name.Namespace rather than restating the
// mapping so that the tracking table an extension's migrations record into
// can never drift from the table prefix and role name the same unit owns —
// they are one namespace, not three conventions that happen to agree.
func NamespaceFor(unit string) (string, error) {
	ns, err := extension.Name(unit).Namespace()
	if err != nil {
		return "", fmt.Errorf("pgmigrate: %w", err)
	}
	return ns, nil
}

func trackingTable(ctx context.Context, conn *pgx.Conn, namespace string) (string, error) {
	// Digits are admitted because an extension namespace carries them
	// (`ext_foo_1`); the set stays exactly what an unquoted SQL identifier
	// holds, since the namespace is interpolated into the statement below and
	// cannot be a parameter.
	for i, r := range namespace {
		digit := r >= '0' && r <= '9'
		if (r < 'a' || r > 'z') && r != '_' && !digit {
			return "", fmt.Errorf("pgmigrate: namespace %q: want lower-case letters, digits and underscores", namespace)
		}
		if digit && i == 0 {
			return "", fmt.Errorf("pgmigrate: namespace %q: an identifier cannot start with a digit", namespace)
		}
	}
	if namespace == "" {
		return "", fmt.Errorf("pgmigrate: empty namespace: it keys the tracking table")
	}
	table := "schema_migrations_" + namespace
	_, err := conn.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			version        text PRIMARY KEY,
			name           text NOT NULL,
			applied_at     timestamptz NOT NULL DEFAULT now(),
			content_digest text
		)`, table))
	if err != nil {
		return "", fmt.Errorf("pgmigrate: creating %s: %w", table, err)
	}
	// The tracking tables are created by this function and never by a
	// migration, so a database that already has one predates the column and
	// CREATE TABLE IF NOT EXISTS will not add it. This does.
	//
	// NULLABLE, and it stays that way: a row written before the digest existed
	// records a version whose content nobody can now recover, and back-filling
	// it here would stamp a fingerprint over content this binary never applied
	// — which is precisely the divergence the column exists to expose. A NULL
	// means "unverifiable", and every reader must treat it as such.
	if _, err := conn.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS content_digest text`, table)); err != nil {
		return "", fmt.Errorf("pgmigrate: adding %s.content_digest: %w", table, err)
	}
	return table, nil
}

// appliedVersions returns version → the NAME it was applied under.
//
// The name is read, not just the version, because the ledger is the only place
// a renumber is visible. A version recorded under a different name is a
// database that applied some other migration in that slot — and matching on
// the version alone makes the two indistinguishable, so the migration actually
// sitting there is skipped silently and forever.
func appliedVersions(ctx context.Context, conn *pgx.Conn, table string) (map[string]string, error) {
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT version, name FROM %s`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	done := map[string]string{}
	for rows.Next() {
		var version, name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, err
		}
		done[version] = name
	}
	return done, rows.Err()
}

// assertLedgerMatches refuses when a version was applied under a different
// name than the source carries.
//
// It is a stop, not a warning, because every continuation from here is wrong
// in a way nothing later reports. The migration recorded in that slot is not
// the one on disk, so this run would skip the on-disk one as done; the obvious
// manual repair — inserting the new version's row — leaves the database
// permanently missing whatever the skipped migration created, with no failure
// to point at it. A renumbered migration cannot be reconciled forward: the
// database has to be rebuilt (make dev-fresh).
func assertLedgerMatches(namespace string, done map[string]string, m Migration) error {
	recorded, ok := done[m.Version]
	if !ok || recorded == m.Name {
		return nil
	}
	return fmt.Errorf(
		"pgmigrate: %s %s: applied as %q, but the source at that version is %q — this database "+
			"applied a migration that has since been renumbered, so %q would be skipped as done. "+
			"It cannot be repaired forward; rebuild the database (make dev-fresh)",
		namespace, m.Version, recorded, m.Name, m.Name)
}

func inTx(ctx context.Context, conn *pgx.Conn, fn func(pgx.Tx) error) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		// The migration failure is the error the operator must see; the
		// rollback is best-effort cleanup of a transaction that is being
		// abandoned either way.
		//craft:ignore swallowed-errors the migration error being returned supersedes a rollback failure on this abandoned tx
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func lock(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey)
	return err
}

func unlock(ctx context.Context, conn *pgx.Conn) {
	_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
}
