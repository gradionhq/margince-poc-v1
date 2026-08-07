// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SearchEnv is the second EXPORTED fixture here, after Env, and lighter than it:
// one workspace, two reps in two teams, and a search store over the RLS-bound
// pool. (It is not the second fixture in the package — several suites still build
// their own; it is the second one a sibling package can import.)
//
// Most of its riders take it for the migrated database and the seeded workspace
// rather than for the store: the leadscore and export suites reference Store zero
// times, as did the capture and connector suites before they moved out. The name
// records where it was first needed rather than what it is, so read "Search" here
// as the fixture's origin, not as a restriction on who may use it.
type SearchEnv struct {
	Owner *pgx.Conn
	Pool  *pgxpool.Pool
	Store *search.Store
	WS    ids.UUID
	Rep1  ids.UUID // team1
	Rep3  ids.UUID // team2
	Team1 ids.UUID
	Team2 ids.UUID
}

// SetupSearch gives each test a clean, migrated database seeded with the
// SearchEnv fixture. Like Setup it fails loudly without a database rather than
// skipping, and it migrates once per process — see package testdb for what the
// schema and data resets each cost.
func SetupSearch(t *testing.T) *SearchEnv {
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

	e := &SearchEnv{
		Owner: owner, WS: ids.NewV7(),
		Rep1: ids.NewV7(), Rep3: ids.NewV7(), Team1: ids.NewV7(), Team2: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Search', 'search', 'EUR')`, e.WS); err != nil {
		t.Fatal(err)
	}
	for i, u := range []ids.UUID{e.Rep1, e.Rep3} {
		if _, err := owner.Exec(ctx, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
			u, e.WS, fmt.Sprintf("rep%d@search.test", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tm := range []ids.UUID{e.Team1, e.Team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, $3)`, tm, e.WS, tm.String()); err != nil {
			t.Fatal(err)
		}
	}
	for u, tm := range map[ids.UUID]ids.UUID{e.Rep1: e.Team1, e.Rep3: e.Team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`, e.WS, tm, u); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.Pool = pool
	e.Store = search.NewStore(pool)
	return e
}

// Seed writes one row through the owner connection, minting its id and binding
// the workspace. It rides the owner rather than the app pool because the suites
// that use it are testing READ semantics — the write shape has its own suites,
// and going through a store here would make the fixture depend on the thing
// under test.
func (e *SearchEnv) Seed(t *testing.T, sql string, args ...any) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.Owner.Exec(context.Background(), sql, append([]any{id, e.WS}, args...)...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

// searchObjects is the record vocabulary this fixture's principals reach. Named
// once because the read and write grant sets must cover the same objects — if
// they drifted, a suite comparing a reader against a writer would be comparing
// two different vocabularies and attributing the difference to permissions.
var searchObjects = []string{objPerson, objOrg, objDeal, "lead", objActivity}

// searchReadGrants is read on every record type this fixture's principals can
// reach. Read-only on purpose: the suites riding it assert what a caller may SEE,
// so a grant that could write would let a fixture change the rows under its own
// assertion.
func searchReadGrants() map[string]principal.ObjectGrant {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range searchObjects {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	return grants
}

// Admin is an unbounded reader: every record type, row scope all. The fixture's
// positive control — what a caller with nothing hidden from them sees — against
// which AsTeamRep's narrower view is the assertion.
func (e *SearchEnv) Admin() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: searchReadGrants(), RowScope: principal.RowScopeAll},
	})
}

// AsTeamRep is a rep bounded to one team's rows — the same grants as Admin with
// row scope narrowed, so a suite comparing the two isolates row scope from object
// RBAC rather than confounding them.
func (e *SearchEnv) AsTeamRep(user, team ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     []ids.UUID{team},
		Permissions: principal.Permissions{Objects: searchReadGrants(), RowScope: principal.RowScopeTeam},
	})
}

// AsFullUser is a human who may WRITE every record type, unbounded by row scope
// — Admin's mutating counterpart, for the suites whose subject is an ingest or a
// scoring pass rather than a visibility rule. Same object vocabulary as the
// reader, so a suite that swaps one for the other varies only the permission.
func (e *SearchEnv) AsFullUser() context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range searchObjects {
		grants[object] = principal.ObjectGrant{Create: true, Read: true, Update: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}
