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
// The doc names TWO shapes and the gate closes both, so both are exercised.
// Each fixture puts a fully correct scaffolded table in front of the
// desynchronising statement, so the only thing left for the gate to object to
// is the statement the textual rule went blind to.
func TestGateCatchesATableHiddenByAMaskerDesync(t *testing.T) {
	// declaredTables returns [note] with no error: the E'it\'s' default
	// desynchronises the literal masker, which then blanks the rest of the file
	// — including the CREATE TABLE after it.
	t.Run("backslash-escaped quote in an E string", func(t *testing.T) {
		unit := unitName(t, "desync")
		ns := namespaceOf(t, unit)
		up := scaffoldUp(ns) + fmt.Sprintf(`
ALTER TABLE ext.%[1]s_note ADD COLUMN label text NOT NULL DEFAULT E'it\'s';
CREATE TABLE ext.stowaway (id uuid NOT NULL PRIMARY KEY);
`, ns)
		down := "DROP TABLE IF EXISTS ext.stowaway;\n" + scaffoldDown(ns)

		requireRefusal(t, runGate(t, unit, migrationDir(t, up, down)),
			"ext.stowaway", "outside the unit's namespace")
	})

	// declaredTables returns [note it] with no error — WRONG in both
	// directions. The quote inside ext."<ns>_it's" opens a literal for the
	// masker, so createTablePattern captures the truncated `ext."<ns>_it`, which
	// reads as a legitimate table named `it`; and the literal then runs on until
	// the policy's first quote, blanking the unnamespaced CREATE TABLE in
	// between. A table the unit never declared is recorded, and one it did
	// declare is missed.
	t.Run("quote inside a double-quoted identifier", func(t *testing.T) {
		unit := unitName(t, "dquote")
		ns := namespaceOf(t, unit)
		quoted := fmt.Sprintf(`ext."%s_it's"`, ns)
		up := scaffoldUp(ns) + fmt.Sprintf(`
CREATE TABLE %[2]s (
    id           uuid NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE
);
CREATE TABLE ext.stowaway2 (id uuid NOT NULL PRIMARY KEY);
ALTER TABLE %[2]s ENABLE ROW LEVEL SECURITY;
ALTER TABLE %[2]s FORCE ROW LEVEL SECURITY;
CREATE POLICY %[1]s_quoted_isolation ON %[2]s
    USING      %[3]s
    WITH CHECK %[3]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON %[2]s TO margince_app;
`, ns, quoted, predicateSQL)
		down := "DROP TABLE IF EXISTS ext.stowaway2;\nDROP TABLE IF EXISTS " + quoted + ";\n" + scaffoldDown(ns)

		requireRefusal(t, runGate(t, unit, migrationDir(t, up, down)),
			"ext.stowaway2", "outside the unit's namespace")
	})
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
