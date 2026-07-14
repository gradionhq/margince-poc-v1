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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type rosterUser struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	IsAgent     bool   `json:"is_agent"`
}

type rosterTeam struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
}

// wsID resolves a workspace's id by slug through the owner connection
// (workspace is the one non-tenant table, so no GUC is needed to read it).
func wsID(t *testing.T, e *env, slug string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&id); err != nil {
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
// owner must set app.workspace_id even to insert. Mirrors setWorkspaceSeat.
func seedInWorkspace(t *testing.T, e *env, ws ids.UUID, stmts ...seedStmt) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
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
	e := setup(t)
	e.bootstrapWorkspace(t) // workspace "fable-e2e" + admin ada@example.com, session in the jar

	wsA := wsID(t, e, e.slug)
	rep, bob, deskTeam := ids.NewV7(), ids.NewV7(), ids.NewV7()
	seedInWorkspace(
		t, e, wsA,
		stmt(`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'rep@example.com', 'Rep One')`, rep, wsA),
		stmt(`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'bob@example.com', 'Bob Two')`, bob, wsA),
		stmt(`INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, 'Deal Desk')`, deskTeam, wsA),
		stmt(`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($2, $1, $3)`, deskTeam, wsA, rep),
		stmt(`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($2, $1, $3)`, deskTeam, wsA, bob),
	)

	// A second tenant with its own member — its rows must never surface
	// under workspace A's session (RLS row-scope). Seed workspace B (a
	// non-tenant row) then its user inside B's GUC.
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Other', 'fable-other', 'EUR')`,
		ids.NewV7()); err != nil {
		t.Fatalf("seeding workspace B: %v", err)
	}
	wsB := wsID(t, e, "fable-other")
	seedInWorkspace(
		t, e, wsB,
		stmt(`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'eve@other.example', 'Eve Other')`, ids.NewV7(), wsB),
	)

	// (e) No session → 401, before we lean on the authenticated reads.
	assertRosterUnauthorized(t, e)

	// (a) The roster lists workspace A's members: the bootstrap admin plus
	// the two seeded reps, and nothing else.
	var users struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/users", nil, nil, &users); status != http.StatusOK {
		t.Fatalf("list users → %d, want 200", status)
	}
	got := map[string]rosterUser{}
	for _, u := range users.Data {
		got[u.Email] = u
	}
	for _, want := range []string{"ada@example.com", "rep@example.com", "bob@example.com"} {
		if _, ok := got[want]; !ok {
			t.Errorf("roster missing %q; got %+v", want, users.Data)
		}
	}
	// (b) Workspace isolation: B's member never appears.
	if _, leaked := got["eve@other.example"]; leaked {
		t.Error("cross-tenant leak: workspace B's user appears in workspace A's roster")
	}
	if len(users.Data) != 3 {
		t.Fatalf("roster size = %d, want exactly the 3 workspace-A members: %+v", len(users.Data), users.Data)
	}
	// workspace_id is required on User and must be the caller's workspace.
	for _, u := range users.Data {
		if u.WorkspaceID != wsA.String() {
			t.Errorf("user %q workspace_id = %q, want %q", u.Email, u.WorkspaceID, wsA)
		}
	}

	// (c) q narrows over display_name/email, case-insensitively.
	var filtered struct {
		Data []rosterUser `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/users?q=REP", nil, nil, &filtered); status != http.StatusOK {
		t.Fatalf("list users?q=REP → %d, want 200", status)
	}
	if len(filtered.Data) != 1 || filtered.Data[0].Email != "rep@example.com" {
		t.Fatalf("q=REP → %+v, want only rep@example.com", filtered.Data)
	}

	// (d) Teams carry the active membership count.
	var teams struct {
		Data []rosterTeam `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/teams", nil, nil, &teams); status != http.StatusOK {
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
	if desk.WorkspaceID != wsA.String() {
		t.Errorf("Deal Desk workspace_id = %q, want %q", desk.WorkspaceID, wsA)
	}
}

// assertRosterUnauthorized issues a session-less request (the TLS-trusting
// transport, but no cookie jar) against each roster endpoint and expects a
// 401 — both /v1/users and /v1/teams are authenticated-only, and either
// could lose that gate independently, so both are exercised.
func assertRosterUnauthorized(t *testing.T, e *env) {
	t.Helper()
	noSession := &http.Client{Transport: e.client.Transport}
	for _, path := range []string{"/v1/users", "/v1/teams"} {
		req, err := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
		if err != nil {
			t.Fatalf("building request for %s: %v", path, err)
		}
		req.Header.Set("X-Workspace-Slug", e.slug)
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
