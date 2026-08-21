// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

// The archived-tenant gate (0272) refuses, and refuses for the right reason.
//
// A gate is the one kind of migration that passes its whole life doing nothing:
// every installation this ships to has no archived residue, so nothing exercises
// the refusal, and a gate that stopped refusing would look exactly like one that
// had nothing to refuse. This suite is the only thing that can tell those apart.
//
// It re-runs the shipped migration's own SQL rather than a paraphrase, which is
// the pattern this repository retired for the 0148/0149 data repairs — and the
// difference is worth naming. Those named columns, so a later schema change made
// the replay a question production never asks. This one reads pg_catalog for
// whatever still carries workspace_id, so it is schema-agnostic by construction:
// as phase D removes columns the gate simply has less to check, and never less
// truth to tell.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// archivedGateVersion is the migration under test. Named once so a renumber is
// one edit and a wrong number is a loud "no such migration" rather than a
// silently skipped suite.
const archivedGateVersion = "0272"

// gateSQL is the shipped up-migration's text, loaded from the embedded
// namespace. A copy pasted here would drift from the file that actually runs,
// which is the whole failure mode this suite exists to prevent.
func gateSQL(t *testing.T) string {
	t.Helper()
	core, _ := namespaces(t)
	for _, m := range core.Migrations {
		if m.Version == archivedGateVersion {
			return m.UpSQL
		}
	}
	t.Fatalf("core migration %s is not in the namespace — renumbered, or removed without removing this suite", archivedGateVersion)
	return ""
}

// archivedRowsSeeded is how many rows the fixture plants, and it is deliberately
// not one: a gate that reported "1 rows" for any amount would pass a test that
// only ever seeded one.
const archivedRowsSeeded = 2

// seedArchivedTenantHolding plants an archived workspace and the rows it owns in
// a table that still carries the tenant column, and returns the table's name.
//
// The subject is a FIXTURE table, and that is the point rather than a shortcut.
// The gate derives its subjects from the catalog — every table that still has
// workspace_id, minus the two append-only ledgers it excludes by name. ADR-0091
// §8 phase D has now taken the column off every other table, so the gate has no
// REAL subject left: pointed at anything the schema actually contains it can
// only pass, which is indistinguishable from a gate that no longer works.
//
// What still has to hold is the mechanism — that a table carrying the column
// and holding an archived tenant's rows is FOUND and NAMED. A table created
// here is the only way left to state that, and it is also the case the gate
// exists for: the next table added with a workspace_id is exactly what it must
// catch.
func seedArchivedTenantHolding(ctx context.Context, t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		CREATE TABLE archived_residue_fixture (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
			note         text NOT NULL
		)`); err != nil {
		t.Fatalf("creating the fixture table the gate must find: %v", err)
	}
	var ws string
	if err := conn.QueryRow(ctx, `
		INSERT INTO workspace (slug, archived_at) VALUES ('gone-tenant', now()) RETURNING id`).Scan(&ws); err != nil {
		t.Fatalf("seeding the archived workspace: %v", err)
	}
	for i := range archivedRowsSeeded {
		if _, err := conn.Exec(ctx, `
			INSERT INTO archived_residue_fixture (workspace_id, note)
			VALUES ($1, $2)`, ws, fmt.Sprintf("ghost-%d", i)); err != nil {
			t.Fatalf("seeding the archived tenant's row %d: %v", i, err)
		}
	}
	return "archived_residue_fixture"
}

func TestTheArchivedResidueGateRefusesAndNamesWhatItFound(t *testing.T) {
	ctx := context.Background()
	conn := connect(t, mustOwnerDSN(t))
	headSchema(t, conn)

	table := seedArchivedTenantHolding(ctx, t, conn)

	_, err := conn.Exec(ctx, gateSQL(t))
	if err == nil {
		t.Fatal("the gate admitted an installation whose archived tenant still holds records — phase D would merge them into this installation's own, and no rollback separates them again")
	}
	// The message has to be actionable on its own: an operator meets it in a
	// deploy log with no access to this test.
	if !strings.Contains(err.Error(), table) {
		t.Errorf("the refusal is %q, which does not name %s — an operator cannot act on a refusal that will not say what it found", err, table)
	}
	// The COUNT as well as the table: it is what tells an operator whether they
	// are about to delete two rows or two hundred thousand, and a gate that
	// counted wrong would still name the right table.
	if want := fmt.Sprintf("%d rows", archivedRowsSeeded); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal is %q, which does not report %q — the number is what sizes the decision it is asking for", err, want)
	}
}

// The gate lets an installation through once the residue is gone, which is the
// other half of its promise: "decide, then run it again" is a lie if the second
// run refuses too.
func TestTheArchivedResidueGateAdmitsOnceTheResidueIsCleared(t *testing.T) {
	ctx := context.Background()
	conn := connect(t, mustOwnerDSN(t))
	headSchema(t, conn)

	seedArchivedTenantHolding(ctx, t, conn)
	if _, err := conn.Exec(ctx, gateSQL(t)); err == nil {
		t.Fatal("the gate admitted the residue it exists to refuse — the pass below would prove nothing")
	}

	if _, err := conn.Exec(ctx, `
		DELETE FROM archived_residue_fixture WHERE workspace_id IN (SELECT id FROM workspace WHERE archived_at IS NOT NULL)`); err != nil {
		t.Fatalf("clearing the archived tenant's rows: %v", err)
	}
	if _, err := conn.Exec(ctx, gateSQL(t)); err != nil {
		t.Fatalf("the gate still refuses an installation that did what it asked: %v", err)
	}
}

// An ARCHIVED tenant's ledger rows are not residue the operator can clear — the
// immutability trigger forbids deleting them — so the gate must not demand it.
// BOTH ledgers, because the exemption is a two-name list and a test that drove
// one of them would let the other be dropped from it silently.
func TestTheArchivedResidueGateExemptsTheAppendOnlyLedgers(t *testing.T) {
	ctx := context.Background()
	conn := connect(t, mustOwnerDSN(t))
	headSchema(t, conn)

	var ws string
	if err := conn.QueryRow(ctx, `
		INSERT INTO workspace (slug, archived_at) VALUES ('gone-tenant', now()) RETURNING id`).Scan(&ws); err != nil {
		t.Fatalf("seeding the archived workspace: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, 'system', 'system', 'create', 'person', gen_random_uuid())`, ws); err != nil {
		t.Fatalf("seeding the archived tenant's audit row: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO system_log (workspace_id, actor_type, actor_id, action)
		VALUES ($1, 'system', 'system', 'retention_pass_failed')`, ws); err != nil {
		t.Fatalf("seeding the archived tenant's system_log row: %v", err)
	}

	if _, err := conn.Exec(ctx, gateSQL(t)); err != nil {
		t.Fatalf("the gate refused over an append-only ledger row: %v — the operator cannot delete it, so this demands the impossible", err)
	}
}
