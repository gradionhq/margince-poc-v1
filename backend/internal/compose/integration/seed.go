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

// The row-seeding helpers the Env-riding suites share: fixtures that put records
// in front of a test. Kept apart from the harness boot so each file holds one
// concept — and so growing the seeding vocabulary does not grow harness.go.

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

// SeedStakeholder creates a person, ties them to the deal as a
// deal_stakeholder, and gives them one email in each named direction at
// the fixed instant 2026-06-01T12:00Z — three days before the
// 2026-06-04T12:00Z clock the consuming suites pin.
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
		touch := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'touch', '2026-06-01T12:00:00Z', '`+direction+`', 'manual', 'human:x')`, e.WS)
		LinkActivity(t, owner, e.WS, touch, objPerson, person)
	}
	return person
}

// LinkActivity attaches an activity to a person or deal through the
// polymorphic link table.
func LinkActivity(t *testing.T, owner *pgx.Conn, ws, activity ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	column := "deal_id"
	if entityType == objPerson {
		column = "person_id"
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, `+column+`) VALUES ($1, $2, $3, $4)`,
		ws, activity, entityType, entity); err != nil {
		t.Fatal(err)
	}
}

// SeedRow inserts one row through the owner connection and returns its id.
func SeedRow(t *testing.T, owner *pgx.Conn, sql string, ws ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), sql, id, ws); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

// LinkToOrg attaches an activity directly to an account (LinkActivity above
// covers only the person and deal columns).
func LinkToOrg(t *testing.T, e *Env, activity, org ids.UUID) {
	t.Helper()
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, activity, org)
}

// Employ ties a person to an organization as a current employee.
func Employ(t *testing.T, e *Env, person, org ids.UUID, title string) {
	t.Helper()
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, role, source, captured_by)
		VALUES ($1, 'employment', $2, $3, $4, 'manual', 'human:x')`, e.WS, person, org, title)
}

// AccountMailDirectedAt seeds one message with an explicit direction, which is
// what a last-touch assertion turns on: the same date means opposite things
// depending on who wrote it.
func AccountMailDirectedAt(t *testing.T, owner *pgx.Conn, ws ids.UUID, subject, direction string, at time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `INSERT INTO activity
		(id, workspace_id, kind, direction, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, $2, 'email', $3, $4, $5, $5, 'manual', 'human:x')`,
		id, ws, direction, subject, at); err != nil {
		t.Fatalf("seeding %q: %v", subject, err)
	}
	return id
}
