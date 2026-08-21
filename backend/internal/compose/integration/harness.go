// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Env is the migrated-database fixture every integration suite starts
// from: one workspace, three humans (Rep1+Rep2 share Team1, Rep3 sits in
// Team2), and the core stores over the workspace-bound app pool.
type Env struct {
	Pool       *pgxpool.Pool
	People     *people.Store
	Deals      *deals.Store
	Contracts  *contracts.Store
	Activities *activities.Store
	WS         ids.UUID
	// three humans: Rep1+Rep2 share a team, Rep3 sits in another
	Rep1, Rep2, Rep3 ids.UUID
	// AdminUser is the seat Admin() binds: a real app_user, because a manual
	// create stamps the caller as owner and the owner column is a foreign key.
	//
	// It is also the seat in NO TEAM: the membership seeding below covers Rep1,
	// Rep2 and Rep3 and deliberately not this one, which is what lets a suite own
	// a record by a real-but-teamless seat. Putting it in a team breaks
	// TestARecordWhoseOwnerIsInNoTeamIsCoveredByNoTeam, which asserts the gap
	// rather than assuming it — so the failure names this decision instead of
	// reading as a filter bug.
	AdminUser    ids.UUID
	Team1, Team2 ids.UUID
}

// Setup gives each test a clean, migrated database and seeds the
// workspace/user/team fixture, returning the ready Env. The schema is migrated
// once per test process (testdb.EnsureSchema); every later test resets the data
// only (testdb.Reset) — nothing here remigrates, and nothing truncates the whole
// schema; see package testdb for what each of those costs.
// Integration tests fail loudly without a database — they never skip.
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
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &Env{
		WS: ids.NewV7(), Rep1: ids.NewV7(), Rep2: ids.NewV7(), Rep3: ids.NewV7(),
		AdminUser: ids.NewV7(),
		Team1:     ids.NewV7(), Team2: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, 'authz')`, e.WS); err != nil {
		t.Fatal(err)
	}
	seedInstallationIdentity(ctx, t, owner)
	for i, user := range []ids.UUID{e.Rep1, e.Rep2, e.Rep3, e.AdminUser} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, email, display_name) VALUES ($1, $2, $3)`, user, string(rune('a'+i))+"@authz.test", "Rep"); err != nil {
			t.Fatal(err)
		}
	}
	for _, team := range []ids.UUID{e.Team1, e.Team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team (id, name) VALUES ($1, $2)`, team, team.String()); err != nil {
			t.Fatal(err)
		}
	}
	for user, team := range map[ids.UUID]ids.UUID{e.Rep1: e.Team1, e.Rep2: e.Team1, e.Rep3: e.Team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team_membership (team_id, user_id) VALUES ($1, $2)`,
			team, user); err != nil {
			t.Fatal(err)
		}
	}

	// Shared across the package's tests, and deliberately not closed here — see
	// testdb.Pool for why the connections, not the pool object, are the cost.
	pool, err := testdb.Pool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Registered here, before the test adds any cleanup of its own, so it runs
	// last and sees a package that has genuinely stopped.
	t.Cleanup(func() { testdb.AssertPoolsQuiesced(t) })
	e.Pool = pool
	e.People = people.NewStore(harnessDB(pool, e.WS))
	e.Deals = deals.NewStore(harnessDB(pool, e.WS), installseam.Deals())
	e.Contracts = contracts.NewStore(harnessDB(pool, e.WS))
	e.Activities = activities.NewStore(harnessDB(pool, e.WS))
	return e
}

// SchemaPool opens the owner-privileged schema-change pool the
// customfields engine's DDL transaction rides — the
// integration stand-in for a mounted MARGINCE_SCHEMA_DSN, built from the
// same owner DSN the migration step uses.
func SchemaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// OwnerConn opens the schema-owner connection tests use to shift
// timestamps the app role's workspace-bound path could never touch.
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

// As binds a full operation context for one human principal.
func (e *Env) As(user ids.UUID, teams []ids.UUID, perms principal.Permissions) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, TeamIDs: teams, Permissions: perms,
	})
}

// Admin binds an unbounded admin context under the harness's admin seat. It
// is one real user, not a fresh synthetic one per call: a row the admin
// creates without naming an owner is stamped with this id, and the owner
// column is a foreign key into app_user.
func (e *Env) Admin() context.Context { return e.As(e.AdminUser, nil, AdminPerms) }

// AgentCtx binds a synthetic agent principal for staging (the staging
// path itself is not what a suite using this is testing).
func (e *Env) AgentCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
	})
}

// SeedPassport inserts a live passport for Rep1 and returns its id. Rows
// that reference a passport carry a real foreign key, so a synthetic id
// would be rejected by the database rather than by the code under test.
func (e *Env) SeedPassport(t *testing.T, owner *pgx.Conn, label string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO passport (id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
		VALUES ($1, $2, $2, $3, ARRAY['read','write'], $4, now() + interval '1 day')`,
		id, e.Rep1, label, "hash-"+id.String()); err != nil {
		t.Fatalf("seeding passport %s: %v", label, err)
	}
	return id
}

// AgentCtxWithPassport is AgentCtx carrying a passport id, which is what a
// real agent principal always holds. The distinction matters wherever
// provenance decides authority — a staging with a passport was minted by an
// agent asserting one, not by a server-side proposal flow. Pass an id from
// SeedPassport: rows that record it are foreign-keyed to the real table.
func (e *Env) AgentCtxWithPassport(passportID ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
		PassportID: passportID,
	})
}

// The id wideners assert a harness-seeded untyped id as the entity a people-store
// call targets — the suites' spelling of the contracts-edge ids.From widening. The
// harness keeps its fixture ids untyped so every module's suite can share them,
// and each suite widens at the call it makes.
//
// Only PersonIDOf is exported, because integration/channels widens person ids from
// outside this package. The other three have no caller beyond it, and a suite
// package that later needs one exports it then.

// PersonIDOf widens a harness fixture id to a person id.
func PersonIDOf(u ids.UUID) ids.PersonID { return ids.From[ids.PersonKind](u) }

// orgIDOf widens a harness fixture id to an organization id.
func orgIDOf(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }

// leadIDOf widens a harness fixture id to a lead id.
func leadIDOf(u ids.UUID) ids.LeadID       { return ids.From[ids.LeadKind](u) }
func projectIDOf(u ids.UUID) ids.ProjectID { return ids.From[ids.ProjectKind](u) }

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

// SeedPartnerOrg creates an organization and gives it a partner programme, so
// a deal may name it.
//
// A deal's partner must BE a partner (the store refuses any other company,
// because commission prices from the margin tier on that row). A fixture that
// pointed a deal at a plain organization was writing a row the product itself
// will not write, which is the shape of test that proves nothing.
//
// The tier is optional: a partner with none is a real and common state — the
// arrangement exists, the rate has not been agreed — and accrual treats it as
// earning nothing rather than as an error.
func (e *Env) SeedPartnerOrg(t *testing.T, name string, tier *string, owner *ids.UUID) ids.UUID {
	t.Helper()
	org := e.SeedOrg(t, name, owner)
	// The harness's AdminPerms deliberately carries no `partner` grant, so the
	// seeding acts as a seat that has one. Borrowing the caller's context would
	// make every suite that seeds a partner grow a permission it is not testing.
	seeder := e.As(e.AdminUser, nil, principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"partner": {Create: true, Read: true, Update: true},
			// Becoming a partner also stamps the organization's relationship
			// types, so the seat needs the company as well as the programme.
			objOrg: {Read: true, Update: true},
		},
		RowScope: principal.RowScopeAll,
	})
	if _, err := e.People.UpsertPartner(seeder, people.UpsertPartnerInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		PartnerRole:    "consulting",
		MarginTier:     tier,
	}); err != nil {
		t.Fatalf("giving %s a partner programme: %v", name, err)
	}
	return org
}

// SeedOrgAs creates an ownerless organization in a SECOND workspace, under
// that workspace's own context — unlike SeedOrg, which always writes the
// harness's primary workspace as e.Admin().
//
// It names the workspace as well as the ctx because the row lands wherever the
// STORE is bound: the harness's own store would stamp the second tenant's ids
// into the first tenant's transaction, which RLS refuses.
func (e *Env) SeedOrgAs(ctx context.Context, t *testing.T, ws ids.UUID, name string) ids.UUID {
	t.Helper()
	org, err := e.PeopleFor(ws).CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: name})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(org.Id)
}

// SeedDeal creates a deal owned by the given user, acting as admin.
func (e *Env) SeedDeal(t *testing.T, name string, pipeline ids.PipelineID, stage ids.StageID, owner *ids.UUID) ids.UUID {
	t.Helper()
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: name, PipelineID: pipeline, StageID: stage, OwnerID: userIDPtr(owner),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(d.Id)
}

// MakeCapturePrivate turns a seeded person or organization into a
// capture-private row — `visibility='owner'`, owned by the given user — the
// state a connector leaves an unpromoted contact in. Person, organization,
// lead and deal are otherwise readable by every seat of the workspace, so
// this is the ONE way a test still has to put an identity row out of a
// caller's read scope; a test about row scope on a commercial table seeds a
// project instead.
func (e *Env) MakeCapturePrivate(t *testing.T, table string, id, owner ids.UUID) {
	t.Helper()
	if table != "person" && table != "organization" {
		t.Fatalf("MakeCapturePrivate: %s carries no visibility column", table)
	}
	e.WsExec(t, `UPDATE `+table+` SET visibility = 'owner', owner_id = $2 WHERE id = $1`, id, owner)
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

// AgentWithOrgRead binds an agent principal holding the same object grants
// the rep does, unbounded, and CARRYING the granting human's user id — the
// shape identity/passport.go actually mints, where OnBehalfOf becomes
// UserID for row scope. An agent with no user id would be refused for the
// wrong reason and would prove nothing about the human-only rule.
func AgentWithOrgRead(e *Env) context.Context {
	// Deep copy, not `perms := AccountRepPerms`: a plain struct copy shares the
	// Objects map, and this fixture is now read from other packages. A later
	// grant added here would widen it for every suite at once, which is exactly
	// how a negative test starts passing without testing anything.
	perms := AccountRepPerms
	perms.Objects = make(map[string]principal.ObjectGrant, len(AccountRepPerms.Objects))
	for object, grant := range AccountRepPerms.Objects {
		perms.Objects[object] = grant
	}
	perms.RowScope = principal.RowScopeAll
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
		UserID: e.Rep1, OnBehalfOf: e.Rep1, Permissions: perms,
	})
}

// SchedulerPerms is RepPerms plus the activity grant the booking write
// needs; row scope stays team.
var SchedulerPerms = principal.Permissions{
	RoleKeys: []string{roleRep},
	Objects: map[string]principal.ObjectGrant{
		objPerson:   {Create: true, Read: true, Update: true},
		objActivity: {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// ApplyRiverSchema gives a suite River's schema on the harness-migrated
// database, as cmd/migrate does after core and custom. Every suite that drives a
// real River runner needs it present, and those sit here, in package compose,
// and in the sibling suite packages alike.
//
// Call it AFTER Setup — testdb.EnsureRiverSchema explains why the order matters
// and why the guard probes the table rather than a flag.
func ApplyRiverSchema(t *testing.T) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	// The shared owner pool, not a fresh one: every suite that needs a real
	// worker calls this, and each call is one existence probe. testdb.Pool
	// refuses to open before EnsureSchema has run, so a caller that reached
	// here without a harness Setup is told so rather than served a connection
	// older than the schema.
	ownerPool, err := testdb.Pool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	if err := testdb.EnsureRiverSchema(ctx, ownerPool, jobs.Migrate); err != nil {
		t.Fatal(err)
	}
}

// withFullSignalGrant copies a permission set and adds the whole signal grant,
// which is what the real admin role holds (identity/internal/policy.go). It is
// a copy because principal.Permissions carries a map, and mutating the shared
// fixture would grant signals to every test in the package at once.
func withFullSignalGrant(base principal.Permissions) principal.Permissions {
	out := base
	out.Objects = make(map[string]principal.ObjectGrant, len(base.Objects)+1)
	for object, grant := range base.Objects {
		out.Objects[object] = grant
	}
	out.Objects["signal"] = principal.ObjectGrant{
		Create: true, Read: true, Update: true, Delete: true,
	}
	return out
}

// SeedWonDealLinkedTo files the given activities against a WON deal, which is
// what makes them Handelsbriefe under the statutory correspondence floor
// (A165/ADR-0114).
//
// Before A165 the floor shielded by exclusion — every activity that was not a
// task or a note — so a fixture testing it needed no deal at all. The floor now
// covers correspondence about an actual commercial transaction, so a test that
// wants a shielded record has to supply the transaction. A fixture that skips
// it does not test a weaker floor; it tests the erasure path, because the
// records go.
//
// The deal is written directly rather than through the store because the store
// stamps the correspondence itself on the winning transition, and a fixture
// that used it would prove the stamp works by using the stamp.
func (e *Env) SeedWonDealLinkedTo(t *testing.T, activities ...ids.UUID) ids.UUID {
	t.Helper()
	pipeline, stage, deal := ids.NewV7(), ids.NewV7(), ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `INSERT INTO pipeline (id, name, is_default, position)
			VALUES ($1, 'Floor fixture', false, 90)`, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
			VALUES ($1, $2, 'Closed Won', 0, 'won', 100)`, stage, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deal (id, name, status, pipeline_id, stage_id, closed_at, source, captured_by)
			VALUES ($1, 'Floor fixture deal', 'won', $2, $3, now(), 'manual', 'human:x')`,
			deal, pipeline, stage); err != nil {
			return err
		}
		for _, a := range activities {
			if _, err := tx.Exec(ctx, `INSERT INTO activity_link (activity_id, entity_type, deal_id)
				VALUES ($1, 'deal', $2)`, a, deal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seeding the qualifying deal: %v", err)
	}
	return deal
}
