//go:build integration

package store

// The authorization matrix (B-EP03.2/.3a, features/04 §1 AC): role ×
// object × action × ownership against the real migrated Postgres,
// exercised at the store layer — the one enforcement path HTTP and the
// future MCP surface both ride. Principals are constructed directly (the
// JSONB→Permissions loading path is covered by crm-auth's policy tests).

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/internal/pg"
	"github.com/gradionhq/fable-poc/internal/pgmigrate"
	"github.com/gradionhq/fable-poc/kernel/errs"
	"github.com/gradionhq/fable-poc/kernel/ids"
	"github.com/gradionhq/fable-poc/migrations"
)

type authzEnv struct {
	store *Store
	ws    ids.UUID
	// three humans: rep1+rep2 share a team, rep3 sits in another
	rep1, rep2, rep3 ids.UUID
	team1, team2     ids.UUID
}

func setupAuthz(t *testing.T) *authzEnv {
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
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
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
	if _, err := pgmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	e := &authzEnv{
		ws: ids.NewV7(), rep1: ids.NewV7(), rep2: ids.NewV7(), rep3: ids.NewV7(),
		team1: ids.NewV7(), team2: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Authz', 'authz', 'EUR')`, e.ws); err != nil {
		t.Fatal(err)
	}
	for i, user := range []ids.UUID{e.rep1, e.rep2, e.rep3} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, $4)`,
			user, e.ws, string(rune('a'+i))+"@authz.test", "Rep"); err != nil {
			t.Fatal(err)
		}
	}
	for _, team := range []ids.UUID{e.team1, e.team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, $3)`, team, e.ws, team.String()); err != nil {
			t.Fatal(err)
		}
	}
	for user, team := range map[ids.UUID]ids.UUID{e.rep1: e.team1, e.rep2: e.team1, e.rep3: e.team2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`,
			e.ws, team, user); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := pg.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.store = New(pool)
	return e
}

// permissions fixtures mirror the decisions/0006 matrix rows the test
// exercises; the seeded JSONB↔these shapes is crm-auth's policy tests.
var (
	repPerms = crmctx.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]crmctx.ObjectGrant{
			"person":   {Create: true, Read: true, Update: true},
			"deal":     {Create: true, Read: true, Update: true},
			"pipeline": {Read: true},
		},
		RowScope: crmctx.RowScopeTeam,
	}
	readOnlyPerms = crmctx.Permissions{
		RoleKeys: []string{"read_only"},
		Objects: map[string]crmctx.ObjectGrant{
			"person": {Read: true}, "deal": {Read: true}, "pipeline": {Read: true},
		},
		RowScope: crmctx.RowScopeAll,
	}
	adminPerms = crmctx.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]crmctx.ObjectGrant{
			"person":       {Create: true, Read: true, Update: true, Delete: true},
			"organization": {Create: true, Read: true, Update: true, Delete: true},
			"deal":         {Create: true, Read: true, Update: true, Delete: true},
			"lead":         {Create: true, Read: true, Update: true, Delete: true},
			"activity":     {Create: true, Read: true, Update: true, Delete: true},
			"pipeline":     {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: crmctx.RowScopeAll,
	}
)

// as binds a full operation context for one human principal.
func (e *authzEnv) as(user ids.UUID, teams []ids.UUID, perms crmctx.Permissions) context.Context {
	ctx := crmctx.WithWorkspaceID(context.Background(), e.ws)
	ctx = crmctx.WithCorrelationID(ctx, ids.NewV7())
	return crmctx.WithActor(ctx, crmctx.Principal{
		Type: crmctx.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, TeamIDs: teams, Permissions: perms,
	})
}

func (e *authzEnv) admin() context.Context { return e.as(ids.NewV7(), nil, adminPerms) }

// seedPerson creates a person owned by the given user (nil = ownerless),
// acting as admin.
func (e *authzEnv) seedPerson(t *testing.T, name string, owner *ids.UUID) ids.UUID {
	t.Helper()
	p, err := e.store.CreatePerson(e.admin(), CreatePersonInput{FullName: name, OwnerID: owner, Source: "manual"})
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return ids.UUID(p.Id)
}

func TestObjectLevelRBACDeniesUngrantedActions(t *testing.T) {
	e := setupAuthz(t)
	target := e.seedPerson(t, "Target", &e.rep1)

	reader := e.as(e.rep3, []ids.UUID{e.team2}, readOnlyPerms)

	if _, err := e.store.CreatePerson(reader, CreatePersonInput{FullName: "X", Source: "manual"}); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("read_only create → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.store.UpdatePerson(reader, target, UpdatePersonInput{Title: strPtr("CEO")}); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("read_only update → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.store.ArchivePerson(reader, target); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("read_only archive → %v, want ErrPermissionDenied", err)
	}
	// …but reading is granted, and row_scope=all sees the foreign-owned row.
	if _, err := e.store.GetPerson(reader, target, false); err != nil {
		t.Errorf("read_only get → %v, want success", err)
	}

	// A rep (no delete grant on person) cannot archive even an OWN record:
	// object-level denial precedes row scope.
	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	if _, err := e.store.ArchivePerson(rep, target); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("rep archive own → %v, want ErrPermissionDenied", err)
	}
}

func TestRowScopeTeamNeverShowsAnotherTeamsRecord(t *testing.T) {
	e := setupAuthz(t)
	mine := e.seedPerson(t, "Mine", &e.rep1)
	teammates := e.seedPerson(t, "Teammates", &e.rep2)
	foreign := e.seedPerson(t, "Foreign", &e.rep3)
	shared := e.seedPerson(t, "Shared", nil)

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)

	people, _, err := e.store.ListPeople(rep, ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	visible := map[ids.UUID]bool{}
	for _, p := range people {
		visible[ids.UUID(p.Id)] = true
	}
	for id, want := range map[ids.UUID]bool{mine: true, teammates: true, shared: true, foreign: false} {
		if visible[id] != want {
			t.Errorf("team-scoped list visibility of %s = %v, want %v", id, visible[id], want)
		}
	}

	// Single fetch: the foreign row answers 404 — never the row, and
	// never a 403 that would disclose its existence.
	if _, err := e.store.GetPerson(rep, foreign, false); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("get another team's record → %v, want ErrNotFound", err)
	}
	// Nor can it be mutated blind by id.
	if _, err := e.store.UpdatePerson(rep, foreign, UpdatePersonInput{Title: strPtr("Pwned")}); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("update another team's record → %v, want ErrNotFound", err)
	}

	// row_scope=all (read_only) sees all four.
	all, _, err := e.store.ListPeople(e.as(e.rep3, []ids.UUID{e.team2}, readOnlyPerms), ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("row_scope=all sees %d people, want 4", len(all))
	}
}

func TestMutationRecordsTheGoverningRuleInAuditLog(t *testing.T) {
	e := setupAuthz(t)
	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	p, err := e.store.CreatePerson(rep, CreatePersonInput{FullName: "Audited", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}

	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()

	var rule string
	err = owner.QueryRow(context.Background(),
		`SELECT authorization_rule FROM audit_log WHERE entity_type = 'person' AND entity_id = $1 AND action = 'create'`,
		ids.UUID(p.Id)).Scan(&rule)
	if err != nil {
		t.Fatal(err)
	}
	if want := "role[rep] person.create row_scope=team"; rule != want {
		t.Errorf("authorization_rule = %q, want %q", rule, want)
	}
}

func TestZeroPermissionsFailClosed(t *testing.T) {
	e := setupAuthz(t)
	nobody := e.as(ids.NewV7(), nil, crmctx.Permissions{})
	if _, _, err := e.store.ListPeople(nobody, ListPeopleInput{}); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("unresolved permissions list → %v, want ErrPermissionDenied (fail closed)", err)
	}
}

func strPtr(s string) *string { return &s }
