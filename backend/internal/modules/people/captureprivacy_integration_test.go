// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Capture privacy over a real Postgres. A connector-created person is
// written visibility='owner' (ADR-0063 §7) and stays the capturing user's
// alone until a human promotes it. The rules these tests hold to the
// database, rather than to a rendered predicate string:
//
//   - a teammate under row_scope=team does not read it, though the owner
//     sits in their team and the row would otherwise be theirs to read;
//   - an admin under row_scope=all does not read it either — the founder
//     decision is the importing user ONLY;
//   - the owner reads their own;
//   - promotion to 'workspace' releases it, with no other change;
//   - the list path hides it too, not only the single-row get. A get that
//     404s while the list still returns the row is not privacy.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// privacyEnv is one workspace holding a team of two reps plus an admin, so
// the teammate arm is tested against a colleague who really is in the
// owner's team — an out-of-team miss would pass vacuously.
type privacyEnv struct {
	store    *Store
	ws       ids.UUID
	team     ids.UUID
	owner    ids.UUID
	teammate ids.UUID
	admin    ids.UUID
}

func setupCapturePrivacy(t *testing.T) *privacyEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &privacyEnv{
		ws: ids.NewV7(), team: ids.NewV7(),
		owner: ids.NewV7(), teammate: ids.NewV7(), admin: ids.NewV7(),
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Capture privacy', $2, 'EUR')`,
		e.ws, "cp-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, 'Sales')`,
		e.team, e.ws); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id   ids.UUID
		name string
	}{{e.owner, "Owner"}, {e.teammate, "Teammate"}, {e.admin, "Admin"}} {
		if _, err := conn.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, $4)`,
			u.id, e.ws, "cp-"+u.id.String()+"@cp.test", u.name); err != nil {
			t.Fatal(err)
		}
	}
	// Owner and teammate share a team; the admin does not need one.
	for _, u := range []ids.UUID{e.owner, e.teammate} {
		if _, err := conn.Exec(ctx,
			`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`,
			e.ws, e.team, u); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.store = NewStore(pool)
	return e
}

// as binds one user at one row scope. The teammate carries the shared team
// so their team predicate really does reach the owner's rows.
func (e *privacyEnv) as(user ids.UUID, scope principal.RowScope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	var teams []ids.UUID
	if user == e.owner || user == e.teammate {
		teams = []ids.UUID{e.team}
	}
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs: teams,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"person":       {Create: true, Read: true, Update: true},
				"organization": {Create: true, Read: true, Update: true},
			},
			RowScope: scope,
		},
	})
}

// capturePerson writes one person exactly as connector auto-create does:
// owned by e.owner, visibility='owner', unpromoted.
func (e *privacyEnv) capturePerson(t *testing.T, visibility string) ids.PersonID {
	t.Helper()
	id := ids.New[ids.PersonKind]()
	ctx := e.as(e.owner, principal.RowScopeOwn)
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES ($1, $2, 'Captured Contact', $3, 'gmail:seed', 'connector:gmail', $4)`,
			id, e.ws, e.owner, visibility)
		return err
	}); err != nil {
		t.Fatalf("seeding a %s person: %v", visibility, err)
	}
	return id
}

// canRead answers whether one caller's GetPerson returns the row, failing
// the test on any error that is not the existence-hiding 404.
func (e *privacyEnv) canRead(ctx context.Context, t *testing.T, id ids.PersonID) bool {
	t.Helper()
	_, err := e.store.GetPerson(ctx, id, storekit.LiveOnly)
	switch {
	case err == nil:
		return true
	case errors.Is(err, apperrors.ErrNotFound):
		return false
	default:
		t.Fatalf("reading person %s: %v", id, err)
		return false
	}
}

// listsIt answers whether one caller's ListPeople returns the row. The list
// composes its scope separately from the single-row probe, so a fix that
// only lands on GetPerson would still leak the whole captured book.
func (e *privacyEnv) listsIt(ctx context.Context, t *testing.T, id ids.PersonID) bool {
	t.Helper()
	people, _, err := e.store.ListPeople(ctx, ListPeopleInput{})
	if err != nil {
		t.Fatalf("listing people: %v", err)
	}
	for _, p := range people {
		if ids.UUID(p.Id) == id.UUID {
			return true
		}
	}
	return false
}

func TestAnUnpromotedCapturedPersonIsTheCapturingUsersAlone(t *testing.T) {
	e := setupCapturePrivacy(t)
	captured := e.capturePerson(t, "owner")

	// The owner reads their own capture at every tier they can hold.
	for _, scope := range []principal.RowScope{
		principal.RowScopeOwn, principal.RowScopeTeam, principal.RowScopeAll,
	} {
		ctx := e.as(e.owner, scope)
		if !e.canRead(ctx, t, captured) {
			t.Errorf("the capturing user cannot read their own captured person at row_scope=%s", scope)
		}
		if !e.listsIt(ctx, t, captured) {
			t.Errorf("the capturing user's own captured person is missing from their list at row_scope=%s", scope)
		}
	}

	// A teammate whose team predicate DOES reach the owner's other rows
	// still does not reach this one.
	teammate := e.as(e.teammate, principal.RowScopeTeam)
	if e.canRead(teammate, t, captured) {
		t.Error("a teammate read an unpromoted captured person: the row is the " +
			"capturing user's until a human promotes it (ADR-0063 §7)")
	}
	if e.listsIt(teammate, t, captured) {
		t.Error("a teammate's people list contains an unpromoted captured person")
	}

	// And neither does an admin: capture privacy is a property of the row,
	// not a scope tier.
	admin := e.as(e.admin, principal.RowScopeAll)
	if e.canRead(admin, t, captured) {
		t.Error("an admin read an unpromoted captured person: row_scope=all does " +
			"not clear capture privacy — the decision is the importing user only")
	}
	if e.listsIt(admin, t, captured) {
		t.Error("an admin's people list contains an unpromoted captured person")
	}
}

func TestPromotionReleasesACapturedPersonToTheWorkspace(t *testing.T) {
	e := setupCapturePrivacy(t)
	// The same row, written 'workspace' — the state a human edit or approval
	// promotes it into. Nothing else about the row differs, so a teammate
	// reading it now isolates the visibility column as the only cause.
	promoted := e.capturePerson(t, "workspace")

	teammate := e.as(e.teammate, principal.RowScopeTeam)
	if !e.canRead(teammate, t, promoted) {
		t.Error("a teammate cannot read a promoted captured person: promotion " +
			"released the row and the team predicate should reach it")
	}
	admin := e.as(e.admin, principal.RowScopeAll)
	if !e.canRead(admin, t, promoted) {
		t.Error("an admin cannot read a promoted captured person")
	}
	if !e.listsIt(admin, t, promoted) {
		t.Error("a promoted captured person is missing from an admin's list")
	}
}

func TestAnOutOfTeamRepNeverReachesEitherVisibility(t *testing.T) {
	e := setupCapturePrivacy(t)
	captured := e.capturePerson(t, "owner")
	promoted := e.capturePerson(t, "workspace")

	// The admin holds no team, so at row_scope=team they reach neither row.
	// This is the control: it proves the teammate result above comes from
	// capture privacy and not from an accidentally empty team predicate.
	outsider := e.as(e.admin, principal.RowScopeTeam)
	if e.canRead(outsider, t, captured) {
		t.Error("an out-of-team rep read an owner-private person")
	}
	if e.canRead(outsider, t, promoted) {
		t.Error("an out-of-team rep read another team's person: the owner scope " +
			"itself stopped working")
	}
}
