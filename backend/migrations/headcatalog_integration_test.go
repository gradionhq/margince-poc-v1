// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

// The schema the migrations build, committed, so that changing it is a decision
// somebody made rather than something that happened.
//
// WHAT THIS CATCHES THAT NOTHING ELSE DID. The tracking table records a version,
// not a checksum, and every other gate here asserts a NAMED property — this
// column is generated, that view is security_invoker, this FK carries a
// visibility decision. A migration that alters something no gate happens to name
// lands green, and the schema it produced is legible only by connecting to a
// database and looking. That is also what made the 340-migration history
// unreviewable: nothing in the tree said what schema it was supposed to build,
// so consolidating it could not be checked.
//
// So the expectation lives in the repository. A migration that changes head
// changes testdata/head_catalog.txt in the same commit, and the diff a reviewer
// reads IS the schema effect of the change.
//
// The projection is built from catalogParts, the same query headSchema's
// fingerprint uses, so the two cannot come to cover different object classes.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// updateCatalog rewrites the golden file instead of comparing against it.
//
// A flag rather than an environment variable because `go test` already owns this
// idiom, and the failure message below can print the exact command to run.
var updateCatalog = flag.Bool("update-catalog", false,
	"rewrite backend/migrations/testdata/head_catalog.txt from the migrated schema")

const catalogGolden = "testdata/head_catalog.txt"

func TestMigrationsBuildTheCommittedSchema(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	headSchema(t, conn)

	got := catalogProjection(t, conn)

	if *updateCatalog {
		if err := os.WriteFile(catalogGolden, []byte(got), 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", catalogGolden, err)
		}
		t.Logf("rewrote %s (%d lines) — commit it with the migration that changed it",
			catalogGolden, strings.Count(got, "\n"))
		return
	}

	wantBytes, err := os.ReadFile(catalogGolden)
	if err != nil {
		t.Fatalf("reading %s: %v — regenerate it with "+
			"`go test -tags integration ./migrations -run %s -update-catalog`",
			catalogGolden, err, t.Name())
	}
	want := string(wantBytes)
	if got == want {
		return
	}

	added, removed := lineDiff(want, got)
	t.Errorf("the migrations build a different schema than %s records.\n"+
		"%s\n"+
		"If your migration MEANT to change the schema, regenerate the file and commit it "+
		"with the migration:\n"+
		"    go test -tags integration ./migrations -run %s -update-catalog\n"+
		"If it did not, the migration changed more than you think.",
		catalogGolden, formatDiff(added, removed), t.Name())
}

// catalogProjection is every part catalogParts yields, one per line, sorted.
//
// Sorted rather than in catalog order: an object's oid moves whenever the
// migration that creates it moves, so unsorted output would diff on every
// reordering while describing the same schema.
func catalogProjection(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var projection string
	err := conn.QueryRow(context.Background(),
		catalogParts+`
		SELECT COALESCE(string_agg(part, E'\n' ORDER BY part), '') FROM parts`).Scan(&projection)
	if err != nil {
		t.Fatalf("projecting the schema catalog: %v", err)
	}
	// A trailing newline so the file ends like every other text file in the tree
	// and a diff of the last line is not a diff of the whole last line.
	return projection + "\n"
}

// lineDiff reports which lines only the golden file has and which only the live
// schema has. A set difference, not a positional diff: the projection is sorted,
// so a single inserted object would shift every line after it and report as a
// wholesale rewrite.
func lineDiff(want, got string) (added, removed []string) {
	inWant := map[string]bool{}
	for _, ln := range strings.Split(want, "\n") {
		inWant[ln] = true
	}
	inGot := map[string]bool{}
	for _, ln := range strings.Split(got, "\n") {
		inGot[ln] = true
		if !inWant[ln] && ln != "" {
			added = append(added, ln)
		}
	}
	for _, ln := range strings.Split(want, "\n") {
		if !inGot[ln] && ln != "" {
			removed = append(removed, ln)
		}
	}
	return added, removed
}

// formatDiff caps how much it prints. A migration that drops a column changes a
// handful of lines and all of them are worth reading; one that renames a table
// changes hundreds, and a test failure that scrolls off the screen is one nobody
// reads the top of — which is where the count is.
func formatDiff(added, removed []string) string {
	const show = 20
	var b strings.Builder
	section := func(label string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n  " + label + " (" + strconv.Itoa(len(lines)) + "):\n")
		for i, ln := range lines {
			if i == show {
				b.WriteString("    … and " + strconv.Itoa(len(lines)-show) + " more\n")
				break
			}
			b.WriteString("    " + ln + "\n")
		}
	}
	section("only in the live schema", added)
	section("only in "+filepath.Base(catalogGolden), removed)
	return b.String()
}
