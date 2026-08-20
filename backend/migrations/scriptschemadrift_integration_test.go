// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

// The seed and ops scripts are build artifacts of the schema, and nothing was
// checking that they still match it.
//
// Every ADR-0091 §8 phase D slice sweeps Go string literals for the column it
// removes. scripts/*.sql and scripts/*.sh are not Go, so that sweep never sees
// them: FOUR slices in a row shipped a scripts/seed-dev.sql naming a column
// that had just been dropped. Each was caught by `live-boot`, the slowest job
// on the board and the only one that runs the file — and
// scripts/seed-demo-company.sh, which no job runs at all, was broken on main
// until somebody read it (#1724).
//
// This asserts the property directly, against the migrated schema, in the lane
// that already has one.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// scriptRoots are the trees whose SQL is executed against this schema.
var scriptRoots = []string{"../../scripts", "../../infra"}

var (
	insertColumns = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_]+)\s*\(([^)]*)\)`)
	plainName     = regexp.MustCompile(`^[a-z_]+$`)
)

// TestEveryScriptNamesColumnsTheSchemaHas parses the INSERT column lists out of
// every script and checks each name against the live catalog.
//
// INSERT lists only, deliberately. An identifier in one is unambiguously a
// column of the named table, so this fails loudly with no false positives — and
// catching the INSERTs would have caught all four misses. A predicate
// (`WHERE x.workspace_id = …`) needs statement boundaries to attribute
// correctly, and a checker that cried wolf would be turned off, which is worse
// than one that is narrow.
func TestEveryScriptNamesColumnsTheSchemaHas(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	headSchema(t, conn)

	columns := schemaColumns(t, conn)
	scripts := scriptFiles(t)
	if len(scripts) == 0 {
		t.Fatal("no scripts found to check — the roots moved and this gate now asserts nothing")
	}

	for _, path := range scripts {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, m := range insertColumns.FindAllStringSubmatch(string(body), -1) {
			table := strings.ToLower(m[1])
			known, isTable := columns[table]
			if !isTable {
				// Not a table of this schema: a CTE name, or a table an
				// extension owns. Nothing to say about its columns.
				continue
			}
			for _, raw := range strings.Split(m[2], ",") {
				column := strings.TrimSpace(raw)
				// Anything that is not a bare identifier is a function call or
				// an expression that has wandered in through the regex.
				if !plainName.MatchString(column) {
					continue
				}
				if !known[column] {
					t.Errorf("%s: `INSERT INTO %s` names the column %q, which the migrated schema does not have. "+
						"A script is a build artifact of the schema; this one no longer matches it.",
						filepath.Base(path), table, column)
				}
			}
		}
	}
}

// schemaColumns reads the live catalog as table -> set of column names.
func schemaColumns(t *testing.T, conn *pgx.Conn) map[string]map[string]bool {
	t.Helper()
	rows, err := conn.Query(context.Background(), `
		SELECT c.relname, a.attname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid
		 WHERE n.nspname = 'public' AND c.relkind = 'r'
		   AND a.attnum > 0 AND NOT a.attisdropped`)
	if err != nil {
		t.Fatalf("reading the schema catalog: %v", err)
	}
	defer rows.Close()

	out := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scanning the schema catalog: %v", err)
		}
		if out[table] == nil {
			out[table] = map[string]bool{}
		}
		out[table][column] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the schema catalog: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the migrated schema has no tables — every check below would pass vacuously")
	}
	return out
}

// scriptFiles is every .sql and .sh under the roots above.
func scriptFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range scriptRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if ext := filepath.Ext(path); ext == ".sql" || ext == ".sh" {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	return out
}
