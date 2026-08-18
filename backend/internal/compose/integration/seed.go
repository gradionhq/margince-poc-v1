// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The raw-SQL seeding helpers, as opposed to the store-mediated fixtures on Env
// in harness.go: everything here writes rows with its own INSERT rather than
// going through a module store, which is the line the two files are split on.
//
// That line is load-bearing, not tidiness. This file is the identity-mint site
// backend/dedupespine_test.go sanctions BY PATH, so a direct
// `INSERT INTO person|organization|lead` belongs here and nowhere else in the
// package — put one in harness.go and the gate fails, which is the point.

// DealFixture provisions the workspace with the seeded default pipeline
// and returns the pipeline plus the open + won stage ids.
func DealFixture(t *testing.T, e *Env) (pipeline ids.PipelineID, open, won ids.StageID) {
	t.Helper()
	admin := e.Admin()
	if err := e.Deals.SeedDefaults(admin); err != nil {
		t.Fatal(err)
	}
	p, err := e.Deals.DefaultPipeline(admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range *p.Stages {
		switch st.Semantic {
		case "open":
			if open.IsZero() {
				open = ids.From[ids.StageKind](ids.UUID(st.Id))
			}
		case "won":
			won = ids.From[ids.StageKind](ids.UUID(st.Id))
		}
	}
	return ids.From[ids.PipelineKind](ids.UUID(p.Id)), open, won
}

// stakeholderTouchedAt is the instant SeedStakeholder's emails carry, three
// days before the 2026-06-04T12:00Z clock the consuming suites pin.
var stakeholderTouchedAt = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// SeedStakeholder creates a person, ties them to the deal as a
// deal_stakeholder, and gives them one email in each named direction at
// stakeholderTouchedAt.
func SeedStakeholder(t *testing.T, e *Env, owner *pgx.Conn, deal ids.UUID, directions ...string) ids.UUID {
	t.Helper()
	person := SeedRow(t, owner, `INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Stakeholder', 'manual', 'human:x')`, e.WS)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (workspace_id, kind, person_id, deal_id, source, captured_by)
		 VALUES ($1, 'deal_stakeholder', $2, $3, 'manual', 'human:x')`, e.WS, person, deal); err != nil {
		t.Fatal(err)
	}
	for _, direction := range directions {
		// created_at is pinned alongside occurred_at, not left to the column
		// default: it is part of the activity shape a reader sees, and a
		// wall-clock value there makes an assertion about this fixture depend on
		// when the suite ran. direction is bound rather than interpolated, so a
		// fixture input stays data even when a caller passes something unexpected.
		touch := ids.NewV7()
		if _, err := owner.Exec(context.Background(), `INSERT INTO activity
			(id, kind, subject, occurred_at, created_at, direction, source, captured_by)
			VALUES ($1, 'email', 'touch', $2, $2, $3, 'manual', 'human:x')`,
			touch, stakeholderTouchedAt, direction); err != nil {
			t.Fatalf("seeding %q touch: %v", direction, err)
		}
		LinkActivity(t, owner, touch, "person", person)
	}
	return person
}

// LinkActivity attaches an activity to a person or deal through the
// polymorphic link table.
func LinkActivity(t *testing.T, owner *pgx.Conn, activity ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	column := "deal_id"
	switch entityType {
	case "person":
		column = "person_id"
	case "organization":
		column = "organization_id"
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (activity_id, entity_type, `+column+`) VALUES ($1, $2, $3)`,
		activity, entityType, entity); err != nil {
		t.Fatal(err)
	}
}

// SeedExtraWorkspace mints an additional tenant. archived names a workspace
// nobody looks at any more, which still holds everything it held the day it was
// archived — some passes are owed on it and some deliberately skip it, so a
// fan-out suite states which by seeding one.
//
// Exported because the fan-out suites in sibling packages need a second tenant
// too, and a workspace row is the one thing none of them can mint any other way:
// workspace is outside RLS and no endpoint creates one.
func SeedExtraWorkspace(t *testing.T, owner *pgx.Conn, name string, archived bool) ids.UUID {
	t.Helper()
	ws := ids.NewV7()
	archivedAt := "NULL"
	if archived {
		archivedAt = "now()"
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO workspace (id, slug, archived_at)
		VALUES ($1, $2, `+archivedAt+`)`, ws, name+"-"+ws.String()); err != nil {
		t.Fatalf("seeding the %s workspace: %v", name, err)
	}
	return ws
}

// SeedRow inserts one row through the owner connection and returns its id.
//
// `args` fill $3 onwards. A fixture whose subject is a TIME must pass it here
// rather than write `now() - interval ...` in the SQL: a suite that freezes the
// service's clock and then seeds against the database's is measuring the gap
// between two clocks that drift apart every day it is not run. See
// TestAFrozenClockFixtureDoesNotSeedAgainstTheDatabaseClock.
func SeedRow(t *testing.T, owner *pgx.Conn, sql string, ws ids.UUID, args ...any) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	params := append([]any{id, ws}, args...)
	if _, err := owner.Exec(context.Background(), sql, params...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

// SeedIDRow is SeedRow for a table that no longer has a workspace to bind: it
// mints the id and nothing else. ADR-0091 §8 phase D removes the column table
// by table, so the set of fixtures needing this form only grows — and when the
// last table loses it, this becomes the only form and SeedRow goes.
func SeedIDRow(t *testing.T, owner *pgx.Conn, sql string, args ...any) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), sql, append([]any{id}, args...)...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

// LinkToOrg attaches an activity directly to an account (LinkActivity above
// covers only the person and deal columns).
func LinkToOrg(t *testing.T, e *Env, activity, org ids.UUID) {
	t.Helper()
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, organization_id)
		VALUES ($1, 'organization', $2)`, activity, org)
}

// AccountMailDirectedAt seeds one message with an explicit direction, which is
// what a last-touch assertion turns on: the same date means opposite things
// depending on who wrote it.
func AccountMailDirectedAt(t *testing.T, owner *pgx.Conn, ws ids.UUID, subject, direction string, at time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `INSERT INTO activity
		(id, kind, direction, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, 'email', $2, $3, $4, $4, 'manual', 'human:x')`,
		id, direction, subject, at); err != nil {
		t.Fatalf("seeding %q: %v", subject, err)
	}
	return id
}

// WonByImport is the win-evidence answer a FIXTURE gives (ADR-0109 §6).
//
// A deal reaches a won stage only with a signed contract behind it or a stated
// reason why there is none, and a test seeding a won deal has no agreement in
// this database by construction — which is exactly what `imported` means. Every
// suite that wins a deal to set up something else says so through this, rather
// than each one spelling a literal that would drift from the vocabulary.
//
// A test ABOUT the gate does not use this: it states its own answer, because
// the answer is the thing under test.
func WonByImport() *string {
	reason := "imported"
	return &reason
}
