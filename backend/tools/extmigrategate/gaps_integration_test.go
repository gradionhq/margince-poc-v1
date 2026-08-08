// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// gen-composition's textual migration rule
// (backend/tools/gen-composition/extmigrations.go) names THIS gate, by name, as
// what closes the three shapes it admits it cannot see. That is a claim one
// file makes about another, and the only thing that keeps it true is a test
// here that provokes each shape and watches this gate refuse it.
//
// Each fixture below was run through the real declaredTables to confirm the
// textual rule sees nothing wrong (recorded in the task report); what those
// runs return is quoted on each test.

import (
	"fmt"
	"testing"
)

// Gap 1 — "A CREATE TABLE inside a DO $$ … $$ block is masked away as a
// dollar-quoted body yet executes, so the table it creates is invisible here."
//
// declaredTables returns [] with no error for this fixture. The table is
// nevertheless created, without a workspace_id, and every row in it would be
// readable by every workspace.
func TestGateCatchesATableCreatedInsideADoBlock(t *testing.T) {
	unit := unitName(t, "doblk")
	ns := namespaceOf(t, unit)
	up := fmt.Sprintf(`
DO $$
BEGIN
    CREATE TABLE ext.%[1]s_hidden (id uuid NOT NULL PRIMARY KEY);
END $$;
`, ns)
	down := fmt.Sprintf("DROP TABLE IF EXISTS ext.%s_hidden;\n", ns)

	err := runGate(t, unit, migrationDir(t, up, down))
	requireRefusal(t, err, "ext."+ns+"_hidden", "workspace_id")
}

// Gap 2 — "A quote inside a double-quoted identifier (ext.\"it's\") or a
// backslash-escaped quote in an E'…' string desynchronises maskNonCode's
// literal tracking, which can blank real statements after it."
//
// declaredTables returns [note] with no error for this fixture: the E'it\'s'
// default desynchronises the literal masker, which then blanks the rest of the
// file — including the CREATE TABLE after it, which is unnamespaced and would
// collide with any other unit's table of that name.
//
// The scaffolded table is created first and is entirely correct, so the only
// thing left for the gate to object to is the statement the textual rule went
// blind to.
func TestGateCatchesATableHiddenByAMaskerDesync(t *testing.T) {
	unit := unitName(t, "desync")
	ns := namespaceOf(t, unit)
	up := scaffoldUp(ns) + fmt.Sprintf(`
ALTER TABLE ext.%[1]s_note ADD COLUMN label text NOT NULL DEFAULT E'it\'s';
CREATE TABLE ext.stowaway (id uuid NOT NULL PRIMARY KEY);
`, ns)
	down := "DROP TABLE IF EXISTS ext.stowaway;\n" + scaffoldDown(ns)

	err := runGate(t, unit, migrationDir(t, up, down))
	requireRefusal(t, err, "ext.stowaway", "outside the unit's namespace")
}

// Gap 3 — "Only tables are collected. CREATE INDEX, SEQUENCE, VIEW and
// MATERIALIZED VIEW share PostgreSQL's per-schema relation namespace with
// tables … The brief scopes this rule to tables; the catalog gate enumerates
// every relkind."
//
// declaredTables returns [note] with no error for this fixture. The index name
// is not in the unit's namespace, and because indexes and tables share one
// relation namespace it would collide with another unit's table of that name —
// a collision neither unit can see in its own source.
func TestGateCatchesANonTableRelationOutsideTheNamespace(t *testing.T) {
	unit := unitName(t, "relns")
	ns := namespaceOf(t, unit)
	up := scaffoldUp(ns) + "CREATE INDEX other_unit_note_idx ON ext." + ns + "_note (body);\n"
	down := "DROP INDEX IF EXISTS ext.other_unit_note_idx;\n" + scaffoldDown(ns)

	err := runGate(t, unit, migrationDir(t, up, down))
	requireRefusal(t, err, "ext.other_unit_note_idx", "outside the unit's namespace")
}
