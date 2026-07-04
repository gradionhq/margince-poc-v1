// Package pgmigrate applies the repo's SQL migrations. It exists instead
// of golang-migrate because the schema has THREE ownership namespaces
// (ADR-0017: sequential core/, timestamp custom/, per-jurisdiction packs),
// each with its own tracking table and a fixed core-then-custom apply
// order — a shape that would need one golang-migrate instance per
// namespace anyway. See decisions/0002-hand-rolled-migration-runner.md.
package pgmigrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Migration is one reversible schema step: NNNN_name.up.sql + .down.sql.
type Migration struct {
	Version string // "0001" (core, sequential) or "20260620143000" (custom, timestamp)
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

// advisoryLockKey serializes concurrent migrators cluster-wide; the value
// is arbitrary but must never change.
const advisoryLockKey = 74_726_531 // "margince migrate"

// Load reads NNNN_name.up.sql / NNNN_name.down.sql pairs from dir. A
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
		case strings.HasSuffix(name, ".up.sql"):
			suffix = ".up.sql"
		case strings.HasSuffix(name, ".down.sql"):
			suffix = ".down.sql"
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
		if suffix == ".up.sql" {
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
			if done[m.Version] {
				continue
			}
			if err := inTx(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
					return err
				}
				_, err := tx.Exec(ctx,
					fmt.Sprintf(`INSERT INTO %s (version, name) VALUES ($1, $2)`, table),
					m.Version, m.Name)
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
		if !done[m.Version] {
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

func trackingTable(ctx context.Context, conn *pgx.Conn, namespace string) (string, error) {
	for _, r := range namespace {
		if (r < 'a' || r > 'z') && r != '_' {
			return "", fmt.Errorf("pgmigrate: namespace %q: want lower-case letters", namespace)
		}
	}
	table := "schema_migrations_" + namespace
	_, err := conn.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			version    text PRIMARY KEY,
			name       text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`, table))
	if err != nil {
		return "", fmt.Errorf("pgmigrate: creating %s: %w", table, err)
	}
	return table, nil
}

func appliedVersions(ctx context.Context, conn *pgx.Conn, table string) (map[string]bool, error) {
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT version FROM %s`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	done := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		done[v] = true
	}
	return done, rows.Err()
}

func inTx(ctx context.Context, conn *pgx.Conn, fn func(pgx.Tx) error) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
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
