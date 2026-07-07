// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package integration holds the cross-module integration suites — the
// compose charter exercised end to end over a real migrated Postgres —
// and the one shared harness they ride. The harness lives in this
// non-test file so the white-box suites that must stay inside their
// package (compose root, briefs) can import it; it deliberately imports
// modules and platform only, never compose, so no import cycle can form.
package integration

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

// Env is the migrated-database fixture every integration suite starts
// from: one workspace, three humans (Rep1+Rep2 share Team1, Rep3 sits in
// Team2), and the core stores over the RLS-bound app pool.
type Env struct {
	Pool       *pgxpool.Pool
	People     *people.Store
	Deals      *deals.Store
	Activities *activities.Store
	WS         ids.UUID
	// three humans: Rep1+Rep2 share a team, Rep3 sits in another
	Rep1, Rep2, Rep3 ids.UUID
	Team1, Team2     ids.UUID
}

// Setup drops and remigrates the schema, seeds the workspace/user/team
// fixture, and returns the ready Env. Integration tests fail loudly
// without a database — they never skip.
func Setup(t *testing.T) *Env {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatal(err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	e := &Env{
		WS: ids.NewV7(), Rep1: ids.NewV7(), Rep2: ids.NewV7(), Rep3: ids.NewV7(),
		Team1: ids.NewV7(), Team2: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Authz', 'authz', 'EUR')`, e.WS); err != nil {
		t.Fatal(err)
	}
	for i, user := range []ids.UUID{e.Rep1, e.Rep2, e.Rep3} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, $4)`,
			user, e.WS, string(rune('a'+i))+"@authz.test", "Rep"); err != nil {
			t.Fatal(err)
		}
	}
	for _, team := range []ids.UUID{e.Team1, e.Team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, $3)`, team, e.WS, team.String()); err != nil {
			t.Fatal(err)
		}
	}
	for user, team := range map[ids.UUID]ids.UUID{e.Rep1: e.Team1, e.Rep2: e.Team1, e.Rep3: e.Team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`,
			e.WS, team, user); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.Pool = pool
	e.People = people.NewStore(pool)
	e.Deals = deals.NewStore(pool)
	e.Activities = activities.NewStore(pool)
	return e
}

// OwnerConn opens the schema-owner connection tests use to shift
// timestamps the app role's RLS-bound path could never touch.
func OwnerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	return conn
}

// permissions fixtures mirror the decisions/0006 matrix rows the suites
// exercise; the seeded JSONB↔these shapes is identity's policy tests.
var (
	RepPerms = principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"person":   {Create: true, Read: true, Update: true},
			"deal":     {Create: true, Read: true, Update: true},
			"pipeline": {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	ReadOnlyPerms = principal.Permissions{
		RoleKeys: []string{"read_only"},
		Objects: map[string]principal.ObjectGrant{
			"person": {Read: true}, "deal": {Read: true}, "pipeline": {Read: true},
		},
		RowScope: principal.RowScopeAll,
	}
	AdminPerms = principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"person":       {Create: true, Read: true, Update: true, Delete: true},
			"organization": {Create: true, Read: true, Update: true, Delete: true},
			"deal":         {Create: true, Read: true, Update: true, Delete: true},
			"lead":         {Create: true, Read: true, Update: true, Delete: true},
			"activity":     {Create: true, Read: true, Update: true, Delete: true},
			"pipeline":     {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: principal.RowScopeAll,
	}
)

// As binds a full operation context for one human principal.
func (e *Env) As(user ids.UUID, teams []ids.UUID, perms principal.Permissions) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, TeamIDs: teams, Permissions: perms,
	})
}

// Admin binds an unbounded admin context under a fresh synthetic user.
func (e *Env) Admin() context.Context { return e.As(ids.NewV7(), nil, AdminPerms) }

// AgentCtx binds a synthetic agent principal for staging (the staging
// path itself is not what a suite using this is testing).
func (e *Env) AgentCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
	})
}

// personIDOf / orgIDOf / leadIDOf assert a harness-seeded untyped id as
// the entity a people-store call targets — the suites' spelling of the
// contracts-edge ids.From widening (the harness keeps its fixture ids
// untyped so every module's suite can share them).
func personIDOf(u ids.UUID) ids.PersonID    { return ids.From[ids.PersonKind](u) }
func orgIDOf(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }
func leadIDOf(u ids.UUID) ids.LeadID        { return ids.From[ids.LeadKind](u) }

// userIDPtr types an optional harness user id (Env keeps its fixture ids
// untyped so every module's suite can use them) for people's typed inputs.
func userIDPtr(owner *ids.UUID) *ids.UserID {
	if owner == nil {
		return nil
	}
	id := ids.From[ids.UserKind](*owner)
	return &id
}

// SeedPerson creates a person owned by the given user (nil = ownerless),
// acting as admin.
func (e *Env) SeedPerson(t *testing.T, name string, owner *ids.UUID) ids.UUID {
	t.Helper()
	p, err := e.People.CreatePerson(e.Admin(), people.CreatePersonInput{FullName: name, OwnerID: userIDPtr(owner), Source: "manual"})
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return ids.UUID(p.Id)
}

// SeedOrg creates an organization owned by the given user, acting as admin.
func (e *Env) SeedOrg(t *testing.T, name string, owner *ids.UUID) ids.UUID {
	t.Helper()
	org, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: name, OwnerID: userIDPtr(owner),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(org.Id)
}

// SeedDeal creates a deal owned by the given user, acting as admin.
func (e *Env) SeedDeal(t *testing.T, name string, pipeline, stage ids.UUID, owner *ids.UUID) ids.UUID {
	t.Helper()
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: name, PipelineID: pipeline, StageID: stage, OwnerID: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(d.Id)
}

// WsExec runs one setup statement in a workspace-bound transaction (RLS is
// FORCED, so the GUC must be set even for the owner-less test pool).
func (e *Env) WsExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err != nil {
		t.Fatalf("setup exec: %v", err)
	}
}

// WsCount returns a scalar count in a workspace-bound transaction.
func (e *Env) WsCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var n int
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

// DealFixture provisions the workspace with the seeded default pipeline
// and returns the pipeline plus the open + won stage ids.
func DealFixture(t *testing.T, e *Env) (pipeline, open, won ids.UUID) {
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
				open = ids.UUID(st.Id)
			}
		case "won":
			won = ids.UUID(st.Id)
		}
	}
	return ids.UUID(p.Id), open, won
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
		LinkActivity(t, owner, e.WS, touch, "person", person)
	}
	return person
}

// LinkActivity attaches an activity to a person or deal through the
// polymorphic link table.
func LinkActivity(t *testing.T, owner *pgx.Conn, ws, activity ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	column := "deal_id"
	if entityType == "person" {
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

// SchedulerPerms is RepPerms plus the activity grant the booking write
// needs; row scope stays team.
var SchedulerPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":   {Create: true, Read: true, Update: true},
		"activity": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}
