// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package integration holds the cross-module integration suites — the
// compose charter exercised end to end over a real migrated Postgres —
// and the shared harnesses they ride: Env here, and SearchEnv in
// searchenv.go. Both live in non-test files so the white-box suites that
// must stay inside their package (compose root, briefs) can import them;
// both deliberately import modules and platform only, never compose, so no
// import cycle can form.
//
// Suites also live in sibling packages that import this one. That is how the
// lane gets more than one scheduling slot: one package is one slot, and this
// package is large enough to be the lane's long pole on its own. The set of such
// packages is the subdirectories of this one, which is where to look rather than
// a list here that the next split would have to remember to extend.
// Split a group out when it is a closed seam — it rides an EXPORTED
// fixture (Env or SearchEnv) rather than the booted-app fixture in
// e2e_integration_test.go, and it neither needs nor owes an unexported helper
// across the boundary. A helper that two such groups need is promoted here; one
// that only a group needs stays with it.
//
// A fixture is only importable if its METHODS are in a non-test file too: a
// method declared in a _test.go file is not part of the package a sibling
// imports, so a group could hold the fixture and still be unable to build a
// principal context or seed a row. Promote the methods with the type.
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
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// The RBAC object keys the permission fixtures below repeat often enough to
// name. They are identity's policy vocabulary only — deliberately NOT reused for
// the activity_link.entity_type values seed.go writes, which spell some of the
// same words today from a different namespace and are free to diverge.
const (
	objPerson   = "person"
	objActivity = "activity"
	objDeal     = "deal"
	objOrg      = "organization"
)

// permissions fixtures mirror the RBAC matrix rows the suites
// exercise; the seeded JSONB↔these shapes is identity's policy tests.
var (
	RepPerms = principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			objPerson:  {Create: true, Read: true, Update: true},
			objDeal:    {Create: true, Read: true, Update: true},
			"pipeline": {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	// AccountRepPerms is the rep the account sections are read by: the
	// organization itself, its people and deals, its activities, and the tag/list
	// chips. It is a fixture in its own right rather than RepPerms plus a delta —
	// RepPerms stays narrow because several suites read it as a rep who CANNOT
	// see an organization, and widening it would make those pass while proving
	// nothing. Row scope stays team for the same reason: the interesting failures
	// here are row-scope ones, and an unbounded admin short-circuits every clause.
	AccountRepPerms = principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			objOrg:      {Read: true},
			objPerson:   {Create: true, Read: true, Update: true},
			objDeal:     {Create: true, Read: true, Update: true},
			objActivity: {Create: true, Read: true, Update: true},
			"pipeline":  {Read: true},
			"tag":       {Read: true},
			"list":      {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	ReadOnlyPerms = principal.Permissions{
		RoleKeys: []string{"read_only"},
		Objects: map[string]principal.ObjectGrant{
			objPerson: {Read: true}, objDeal: {Read: true}, "pipeline": {Read: true},
		},
		RowScope: principal.RowScopeAll,
	}
	// AdminWithSignals is AdminPerms plus the warm-room signal grants the real
	// admin role holds (identity/internal/policy.go). It is separate rather
	// than folded in because several tests read AdminPerms as "an admin who
	// cannot see signals" to prove a section is withheld — a fixture that
	// granted everything would make those pass without testing anything.
	AdminWithSignals = withFullSignalGrant(AdminPerms)
	AdminPerms       = principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			objPerson:   {Create: true, Read: true, Update: true, Delete: true},
			objOrg:      {Create: true, Read: true, Update: true, Delete: true},
			objDeal:     {Create: true, Read: true, Update: true, Delete: true},
			"lead":      {Create: true, Read: true, Update: true, Delete: true},
			objActivity: {Create: true, Read: true, Update: true, Delete: true},
			"pipeline":  {Create: true, Read: true, Update: true, Delete: true},
			// computed_field is read-only for every system role, admin
			// included (RD-AC-7: no runtime formula-authoring surface
			// exists) — identity/internal/policy.go's real seed, mirrored
			// here so the harness's admin fixture matches production.
			"computed_field": {Read: true},
			// fx_rate + ai_model_rate are admin/ops-only config surfaces
			// (identity/internal/policy.go's real seed), mirrored here so
			// the harness admin fixture can exercise the rate editors.
			"fx_rate":       {Create: true, Read: true, Update: true, Delete: true},
			"ai_model_rate": {Create: true, Read: true, Update: true, Delete: true},
			"project":       {Create: true, Read: true, Update: true, Delete: true},
			"relationship":  {Create: true, Read: true, Update: true, Delete: true},
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

// SeedPassport inserts a live passport for Rep1 and returns its id. Rows
// that reference a passport carry a real foreign key, so a synthetic id
// would be rejected by the database rather than by the code under test.
func (e *Env) SeedPassport(t *testing.T, owner *pgx.Conn, label string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO passport (id, workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
		VALUES ($1, $2, $3, $3, $4, ARRAY['read','write'], $5, now() + interval '1 day')`,
		id, e.WS, e.Rep1, label, "hash-"+id.String()); err != nil {
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

// personIDOf / orgIDOf / leadIDOf assert a harness-seeded untyped id as
// the entity a people-store call targets — the suites' spelling of the
// contracts-edge ids.From widening (the harness keeps its fixture ids
// untyped so every module's suite can share them).
func personIDOf(u ids.UUID) ids.PersonID    { return ids.From[ids.PersonKind](u) }
func orgIDOf(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }
func leadIDOf(u ids.UUID) ids.LeadID        { return ids.From[ids.LeadKind](u) }
func projectIDOf(u ids.UUID) ids.ProjectID  { return ids.From[ids.ProjectKind](u) }

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

// SeedOrgAs creates an ownerless organization under the caller's own
// context — unlike SeedOrg (always e.Admin(), the harness's one primary
// workspace), this lets a cross-tenant suite seed a fixture in a SECOND
// workspace's own context.
func (e *Env) SeedOrgAs(ctx context.Context, t *testing.T, name string) ids.UUID {
	t.Helper()
	org, err := e.People.CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: name})
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

// SchedulerPerms is the booking write's caller: the person grant it reads and
// the activity grant it writes, and nothing else — not a superset of RepPerms,
// which also holds deal and pipeline. Row scope stays team.
var SchedulerPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		objPerson:   {Create: true, Read: true, Update: true},
		objActivity: {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// ApplyRiverSchema gives this package's suites River's schema on the
// harness-migrated database, as cmd/migrate does after core and custom. Several
// suites here and in package compose drive a real River runner and each needs it
// present.
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
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
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
