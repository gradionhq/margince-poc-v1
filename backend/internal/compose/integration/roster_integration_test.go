// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The workspace roster reads (A52 sharing needs a subject picker + name
// resolution) over the real handler stack + migrated Postgres: any
// authenticated member reads the member/team lists, the lists are
// row-scoped to the caller's workspace (a second tenant's rows never
// appear), the q filter narrows, teams carry a member_count, and an
// unauthenticated caller is refused.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type rosterUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	IsAgent     bool   `json:"is_agent"`
	// A pointer so "the field was withheld" stays distinguishable from "this
	// member holds no role" — the whole point of the admin-only disclosure.
	Roles *[]string `json:"roles"`
}

type rosterTeam struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
}

// wsID resolves a workspace's id by slug through the owner connection
// (workspace is the one non-tenant table, so no GUC is needed to read it).
func wsID(t *testing.T, e *apptest.AppEnv, slug string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := e.Owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("resolving workspace %q: %v", slug, err)
	}
	return id
}

// seedStmt is one workspace-scoped setup statement for seedInWorkspace.
type seedStmt struct {
	sql  string
	args []any
}

func stmt(sql string, args ...any) seedStmt { return seedStmt{sql: sql, args: args} }

// seedInWorkspace runs setup statements inside a workspace-bound
// transaction: app_user/team/team_membership are FORCE-RLS tables, so the
// owner must set app.workspace_id even to insert. Mirrors SetWorkspaceSeat.
func seedInWorkspace(t *testing.T, e *apptest.AppEnv, ws ids.UUID, stmts ...seedStmt) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.Owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, ws.String()); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed exec %q: %v", s.sql, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestRosterReadsUsersAndTeams(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t) // workspace "fable-e2e" + admin ada@example.com, session in the jar

	wsA := wsID(t, e, e.Slug)
	rep, bob, deskTeam := ids.NewV7(), ids.NewV7(), ids.NewV7()
	seedInWorkspace(
		t, e, wsA,
		stmt(`INSERT INTO app_user (id, email, display_name) VALUES ($1, 'rep@example.com', 'Rep One')`, rep),
		stmt(`INSERT INTO app_user (id, email, display_name) VALUES ($1, 'bob@example.com', 'Bob Two')`, bob),
		stmt(`INSERT INTO team (id, name) VALUES ($1, 'Deal Desk')`, deskTeam),
		stmt(`INSERT INTO team_membership (team_id, user_id) VALUES ($1, $2)`, deskTeam, rep),
		stmt(`INSERT INTO team_membership (team_id, user_id) VALUES ($1, $2)`, deskTeam, bob),
	)

	// A second tenant with its own member — its rows must never surface
	// under workspace A's session (RLS row-scope). Seed workspace B (a
	// non-tenant row) then its user inside B's GUC.
	if _, err := e.Owner.Exec(context.Background(),
		`INSERT INTO workspace (id, slug) VALUES ($1, 'fable-other')`,
		ids.NewV7()); err != nil {
		t.Fatalf("seeding workspace B: %v", err)
	}
	// A second workspace's member used to be seeded here, holding a uniquely
	// keyed role, so the assertions below could prove neither escaped into this
	// roster. ADR-0091 §8 phase D took the tenant column off app_user, role and
	// role_assignment, and an installation serves one organization (ADR-0061):
	// there is no second workspace's member to leak, so the arms that looked for
	// one are gone rather than weakened. What the page still owes — every member
	// listed once, the agent seat marked, role keys withheld from a non-admin —
	// is asserted below and unchanged.

	// (e) No session → 401, before we lean on the authenticated reads.
	assertRosterUnauthorized(t, e)

	// (a) The roster lists workspace A's members: the bootstrap admin, the two
	// seeded reps, the agent seat bootstrap writes beside them, and nothing else.
	//
	// The agent seat is LISTED rather than filtered out, because it owns records
	// — the spec calls it the human-visible owner of agent-created ones — so a
	// client resolving an owner id has to be able to find it, and `is_agent` on
	// User is what tells a picker of humans to leave it out. Filtering it here
	// would make that flag unreachable on the one row it describes, and would
	// hide from an admin both that the identity exists and that anything has
	// been done to it.
	var users struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users", nil, nil, &users); status != http.StatusOK {
		t.Fatalf("list users → %d, want 200", status)
	}
	got := map[string]rosterUser{}
	for _, u := range users.Data {
		got[u.Email] = u
	}
	agentSeat := "agent@" + e.Slug + ".gradion.local"
	for _, want := range []string{"ada@example.com", "rep@example.com", "bob@example.com", agentSeat} {
		if _, ok := got[want]; !ok {
			t.Errorf("roster missing %q; got %+v", want, users.Data)
		}
	}
	// The one non-human row says so. A client that cannot tell it apart would
	// offer it wherever it offers a person — as an assignee, as a recipient.
	if seat, listed := got[agentSeat]; listed && !seat.IsAgent {
		t.Errorf("the agent seat is listed with is_agent = false, so nothing on the wire "+
			"distinguishes it from a colleague: %+v", seat)
	}
	if len(users.Data) != 4 {
		t.Fatalf("roster size = %d, want exactly the 4 members: %+v", len(users.Data), users.Data)
	}
	// The role aggregate still has to be EMITTED for an admin. Counted rather
	// than inspected, because the withholding assertion further down is only
	// meaningful against a page that carries keys in the first place.
	keysSeen := 0
	for _, u := range users.Data {
		if u.Roles == nil {
			continue
		}
		keysSeen += len(*u.Roles)
	}
	if keysSeen == 0 {
		t.Fatal("no role keys on the admin roster at all, so the withholding check below would pass vacuously")
	}

	// (c) q narrows over display_name/email, case-insensitively.
	var filtered struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users?q=REP", nil, nil, &filtered); status != http.StatusOK {
		t.Fatalf("list users?q=REP → %d, want 200", status)
	}
	if len(filtered.Data) != 1 || filtered.Data[0].Email != "rep@example.com" {
		t.Fatalf("q=REP → %+v, want only rep@example.com", filtered.Data)
	}

	// (d) Teams carry the active membership count.
	var teams struct {
		Data []rosterTeam `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/teams", nil, nil, &teams); status != http.StatusOK {
		t.Fatalf("list teams → %d, want 200", status)
	}
	var desk *rosterTeam
	for i := range teams.Data {
		if teams.Data[i].Name == "Deal Desk" {
			desk = &teams.Data[i]
		}
	}
	if desk == nil {
		t.Fatalf("teams missing Deal Desk: %+v", teams.Data)
	}
	if desk.MemberCount != 2 {
		t.Errorf("Deal Desk member_count = %d, want 2", desk.MemberCount)
	}
}

// The roster answers every authenticated member, so the role keys it now
// carries need their DENY arm proved where the gate actually lives — at the
// handler, off the request principal. The unit test downstairs proves only that
// the two mappings differ; a regression that inlined the admin mapping into the
// response loop would pass it and leak here.
func TestRosterWithholdsRoleKeysFromANonAdmin(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t) // admin ada@example.com, session in the jar

	wsA := wsID(t, e, e.Slug)
	rep := ids.NewV7()
	seedInWorkspace(
		t, e, wsA,
		stmt(`INSERT INTO app_user (id, email, display_name) VALUES ($1, 'rep@example.com', 'Rep One')`, rep),
		// Borrow the bootstrap admin's hash so the rep can actually sign in:
		// the gate under test reads the request principal, so the assertion is
		// only worth anything from a real non-admin session.
		stmt(`UPDATE app_user SET password_hash = (SELECT password_hash FROM app_user WHERE email = 'ada@example.com') WHERE id = $1`, rep),
		stmt(`INSERT INTO role_assignment (role_id, user_id)
		      SELECT r.id, $1 FROM role r WHERE r.key = 'rep'`, rep),
	)

	// The admin arm first, from the session the bootstrap left: every row
	// carries its keys, and the seeded rep's read back as exactly [rep].
	var asAdmin struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users", nil, nil, &asAdmin); status != http.StatusOK {
		t.Fatalf("admin list users → %d, want 200", status)
	}
	for _, u := range asAdmin.Data {
		if u.Roles == nil {
			t.Fatalf("admin roster: %q carries no roles field", u.Email)
		}
		if u.Email == "rep@example.com" && (len(*u.Roles) != 1 || (*u.Roles)[0] != "rep") {
			t.Errorf("admin roster: rep roles = %v, want [rep]", *u.Roles)
		}
	}

	// Now the deny arm, from the rep's own session.
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email": "rep@example.com", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("rep login → %d, want 200", status)
	}
	var asRep struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users", nil, nil, &asRep); status != http.StatusOK {
		t.Fatalf("rep list users → %d, want 200", status)
	}
	if len(asRep.Data) == 0 {
		t.Fatal("rep roster is empty; the deny arm would pass vacuously")
	}
	for _, u := range asRep.Data {
		if u.Roles != nil {
			t.Errorf("rep sees %q roles = %v; a non-admin must not learn who holds a role", u.Email, *u.Roles)
		}
	}

	// The same principal check gates the widened view — a rep asking for it is
	// answered with the active-only roster, not refused, so this is the only
	// place that failure would show.
	var repWidened struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users?include_inactive=true", nil, nil, &repWidened); status != http.StatusOK {
		t.Fatalf("rep list users (include_inactive) → %d, want 200", status)
	}
	for _, u := range repWidened.Data {
		if u.Roles != nil {
			t.Errorf("rep sees %q roles = %v via include_inactive", u.Email, *u.Roles)
		}
	}
}

// assertRosterUnauthorized issues a session-less request (the TLS-trusting
// transport, but no cookie jar) against each roster endpoint and expects a
// 401 — both /v1/users and /v1/teams are authenticated-only, and either
// could lose that gate independently, so both are exercised.
func assertRosterUnauthorized(t *testing.T, e *apptest.AppEnv) {
	t.Helper()
	noSession := &http.Client{Transport: e.Client.Transport}
	for _, path := range []string{"/v1/users", "/v1/teams"} {
		req, err := http.NewRequest(http.MethodGet, e.TS.URL+path, nil)
		if err != nil {
			t.Fatalf("building request for %s: %v", path, err)
		}
		req.Header.Set("X-Workspace-Slug", e.Slug)
		resp, err := noSession.Do(req)
		if err != nil {
			t.Fatalf("GET %s (no session): %v", path, err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a session → %d, want 401", path, resp.StatusCode)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body for %s: %v", path, err)
		}
	}
}
