// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 1787111736 adds a UNIQUE index to a table that is already allowed to hold
// the rows it forbids, so it ships with a repair. A repair needs a test that
// carries the duplicates through it: applied to a fresh schema the repair
// matches nothing, and a wrong ORDER BY, a wrong predicate or no repair at all
// would every one of them pass.
//
// The precedent for testing a data-carrying migration directly rather than
// through TestMigrations_applyReverseReapply is
// capture_auto_enrich_rollback_integration_test.go, and this follows it,
// including deriving the step count instead of writing a literal.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// employmentRow is one seeded edge and what the repair is expected to do to it.
type employmentRow struct {
	label       string
	person      string // which of the seeded people
	org         string // which of the seeded organizations
	primary     bool
	ended       bool
	survives    bool
	endsPrimary bool
}

// seedEmploymentDuplicates plants the state a deployed database is in on the
// day it applies 1787111736 and returns each row's id by label.
func seedEmploymentDuplicates(t *testing.T, conn *pgx.Conn, rows []employmentRow) map[string]string {
	t.Helper()
	ctx := context.Background()
	people := map[string]string{}
	for _, name := range []string{"duplicated", "unmarked", "undecided"} {
		var id string
		if err := conn.QueryRow(ctx, `
			INSERT INTO person (full_name, source, captured_by)
			VALUES ($1, 'test', 'human:test') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatalf("seeding person %s: %v", name, err)
		}
		people[name] = id
	}
	orgs := map[string]string{}
	for _, name := range []string{"employer", "other"} {
		var id string
		if err := conn.QueryRow(ctx, `
			INSERT INTO organization (display_name, source, captured_by)
			VALUES ($1, 'test', 'human:test') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatalf("seeding organization %s: %v", name, err)
		}
		orgs[name] = id
	}
	ids := map[string]string{}
	for i, row := range rows {
		var ended any
		if row.ended {
			ended = "2020-01-01"
		}
		var id string
		// created_at is set explicitly and DESCENDING, so the oldest row is
		// seeded last: a repair that keeps whatever it happens to reach first
		// cannot pass by accident.
		if err := conn.QueryRow(ctx, `
			INSERT INTO relationship (kind, person_id, organization_id, is_current_primary, ended_at,
			                          source, captured_by, created_at)
			VALUES ('employment', $1, $2, $3, $4::date, 'test', 'human:test', now() - make_interval(mins => $5))
			RETURNING id`,
			people[row.person], orgs[row.org], row.primary, ended, i).Scan(&id); err != nil {
			t.Fatalf("seeding employment %q: %v", row.label, err)
		}
		ids[row.label] = id
	}
	return ids
}

func TestTheEmploymentDedupeIndexRepairsTheDuplicatesItWouldRefuseToApplyOver(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Back to the schema this migration lands on, so the duplicates below are
	// insertable exactly as they were on the day the defect was reported.
	if _, err := dbmigrate.Down(ctx, conn, core, stepsDownTo(t, core, "1787111736")); err != nil {
		t.Fatalf("reverting down to 1787111736: %v", err)
	}

	rows := []employmentRow{
		// Three live current edges for one pair. The primary survives even
		// though it is the NEWEST — demoting somebody's recorded employer to
		// resolve a duplicate would be the repair inventing a fact.
		{label: "primary", person: "duplicated", org: "employer", primary: true, survives: true, endsPrimary: true},
		{label: "duplicate", person: "duplicated", org: "employer", survives: false},
		{label: "oldest duplicate", person: "duplicated", org: "employer", survives: false},
		// An employment they LEFT at the same company is history, not a
		// duplicate — the index's predicate excludes it and so does the repair.
		{label: "former", person: "duplicated", org: "employer", ended: true, survives: true},
		// A concurrent job elsewhere is a different pair entirely, and it is not
		// promoted: this person already has a primary employer.
		{label: "elsewhere", person: "duplicated", org: "other", survives: true},

		// The reported symptom, and the half a unique index alone leaves
		// standing: one employer, no duplicate, and nothing marked primary.
		{label: "sole unmarked", person: "unmarked", org: "employer", survives: true, endsPrimary: true},

		// Two current employers and no primary. Which one wins is a question
		// this migration cannot answer, so it answers neither (#1781).
		{label: "undecided A", person: "undecided", org: "employer", survives: true},
		{label: "undecided B", person: "undecided", org: "other", survives: true},
	}
	seeded := seedEmploymentDuplicates(t, conn, rows)

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("re-applying 1787111736 over duplicates — the repair did not clear the way for the index: %v", err)
	}

	for _, row := range rows {
		var live, primary bool
		if err := conn.QueryRow(ctx,
			`SELECT archived_at IS NULL, is_current_primary FROM relationship WHERE id = $1`,
			seeded[row.label]).Scan(&live, &primary); err != nil {
			t.Fatalf("reading back %q: %v", row.label, err)
		}
		if live != row.survives {
			t.Errorf("%q live = %t, want %t", row.label, live, row.survives)
		}
		if primary != row.endsPrimary {
			t.Errorf("%q current primary = %t, want %t", row.label, primary, row.endsPrimary)
		}
	}

	// The index is the point of the whole migration: a fourth edge for the
	// repaired pair must now be refused rather than added to the pile.
	_, err = conn.Exec(ctx, `
		INSERT INTO relationship (kind, person_id, organization_id, source, captured_by)
		SELECT 'employment', person_id, organization_id, 'test', 'human:test'
		  FROM relationship WHERE id = $1`, seeded["primary"])
	if err == nil {
		t.Fatal("a second live employment at the same company was accepted; uq_rel_employment is not in force")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "uq_rel_employment" {
		t.Fatalf("second employment refused with %v, want a uq_rel_employment unique violation", err)
	}
}
