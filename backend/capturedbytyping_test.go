// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// `captured_by` records the PRINCIPAL, and a principal is not a user row.
//
// The value every store stamps is prefixed by kind — "human:<id>" for a
// session, "agent:<id>" for a passport — because an agent acting under
// somebody's passport has no row in app_user. A column typed `uuid`, or one
// carrying a foreign key to app_user, therefore refuses every write the
// product makes, and the refusal reaches a caller as an internal error.
//
// This is a fitness function rather than a fix, because the defect it catches
// already shipped once: migration 0262 typed the contract table's column
// `uuid REFERENCES app_user(id)` and every contract insert 500'd, with the
// whole gate suite green. Nothing derived the obligation from the tree, so
// nothing noticed that one table disagreed with the other thirty.
//
// It reads the FINAL declaration of each column rather than every historical
// one, because a shipped migration is never edited: 0262 stays wrong forever
// and 0265 is what repairs it. What must hold is the shape an installation
// ends up with, which is the last statement to touch the column.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// capturedByColumn matches a `captured_by` column definition and captures the
// type and the rest of its line, so both the type and any inline FK are read.
var capturedByColumn = regexp.MustCompile(`(?mi)^\s*captured_by\s+(\w+)([^,\n]*)`)

// capturedByAlter matches a later migration retyping the column.
var capturedByAlter = regexp.MustCompile(`(?mis)ALTER\s+COLUMN\s+captured_by\s+TYPE\s+(\w+)`)

// createdTable names the table a migration statement operates on, so a repair
// in a later file is matched against the CREATE that first declared it.
var createdTable = regexp.MustCompile(`(?mi)(?:CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?|ALTER\s+TABLE)\s+(?:public\.)?(\w+)`)

func tableOfMigration(body string) string {
	if m := createdTable.FindStringSubmatch(body); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func TestCapturedByIsAlwaysTextAndNeverAUserForeignKey(t *testing.T) {
	migrations, err := filepath.Glob(filepath.Join("migrations", "core", "*.up.sql"))
	if err != nil {
		t.Fatalf("listing core migrations: %v", err)
	}
	// No floor on the FILE count. core opens with a single baseline holding every
	// table, so "at least N files" would be satisfied by the one file existing
	// and says nothing about whether it was read. The `declared` floor below is
	// the real vacuous-pass guard: it counts captured_by columns actually parsed
	// out, which is what stops working when the glob or the pattern breaks.

	// The last word on each table wins: migrations apply in order, so a later
	// ALTER repairs an earlier CREATE and the final shape is what ships.
	finalType := map[string]string{}
	finalRest := map[string]string{}
	finalSource := map[string]string{}

	var declared int
	for _, path := range migrations {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		table := tableOfMigration(string(body))
		for _, match := range capturedByColumn.FindAllStringSubmatch(string(body), -1) {
			declared++
			finalType[table] = strings.ToLower(match[1])
			finalRest[table] = strings.ToLower(match[2])
			finalSource[table] = filepath.Base(path)
		}
		// An ALTER that retypes the column is the later word on it.
		if m := capturedByAlter.FindStringSubmatch(string(body)); m != nil {
			finalType[table] = strings.ToLower(m[1])
			finalRest[table] = ""
			finalSource[table] = filepath.Base(path)
		}
	}
	if declared < 20 {
		t.Fatalf("only %d captured_by declarations found — the pattern lost its source", declared)
	}

	for table, columnType := range finalType {
		if columnType != "text" {
			t.Errorf("%s leaves captured_by as %q — it holds a prefixed principal "+
				"(\"human:<id>\" / \"agent:<id>\"), so every write against a non-text column fails",
				finalSource[table], columnType)
		}
		if strings.Contains(finalRest[table], "references app_user") {
			t.Errorf("%s leaves captured_by with a foreign key to app_user — an agent acting under "+
				"a passport is not a row there, so the key refuses the write instead of recording who made it",
				finalSource[table])
		}
	}
}
