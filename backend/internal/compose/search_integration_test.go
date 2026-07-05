// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// Ranked cross-object search (B-EP05.15): relevance over the generated
// search_tsv columns, row-scope enforced per branch (a hit IS a read),
// archived rows invisible, stable ranked-keyset pagination, and the
// PERF-3 posture proven structurally — the plan must ride the GIN
// index, not a sequential scan.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type searchEnv struct {
	owner *pgx.Conn
	pool  *pgxpool.Pool
	store *search.Store
	ws    ids.UUID
	rep1  ids.UUID // team1
	rep3  ids.UUID // team2
	team1 ids.UUID
	team2 ids.UUID
}

func setupSearch(t *testing.T) *searchEnv {
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

	e := &searchEnv{
		owner: owner, ws: ids.NewV7(),
		rep1: ids.NewV7(), rep3: ids.NewV7(), team1: ids.NewV7(), team2: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Search', 'search', 'EUR')`, e.ws); err != nil {
		t.Fatal(err)
	}
	for i, u := range []ids.UUID{e.rep1, e.rep3} {
		if _, err := owner.Exec(ctx, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
			u, e.ws, fmt.Sprintf("rep%d@search.test", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tm := range []ids.UUID{e.team1, e.team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, $3)`, tm, e.ws, tm.String()); err != nil {
			t.Fatal(err)
		}
	}
	for u, tm := range map[ids.UUID]ids.UUID{e.rep1: e.team1, e.rep3: e.team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`, e.ws, tm, u); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	e.store = search.NewStore(pool)
	return e
}

// seed writes rows through the owner connection: this suite tests READ
// semantics; the write shape has its own suites.
func (e *searchEnv) seed(t *testing.T, sql string, args ...any) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), sql, append([]any{id, e.ws}, args...)...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

func searchReadGrants() map[string]principal.ObjectGrant {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range []string{"person", "organization", "deal", "lead", "activity"} {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	return grants
}

func (e *searchEnv) admin() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: searchReadGrants(), RowScope: principal.RowScopeAll},
	})
}

func (e *searchEnv) asTeamRep(user ids.UUID, team ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     []ids.UUID{team},
		Permissions: principal.Permissions{Objects: searchReadGrants(), RowScope: principal.RowScopeTeam},
	})
}

// A role with NO person grant gets no person hits — search must not
// out-see the entity lists (object RBAC before row scope).
func TestSearchHonorsObjectRBAC(t *testing.T) {
	e := setupSearch(t)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Rostock Person', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Rostock Werft', 'manual', 'human:x')`)

	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	orgOnly := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep1.String(), UserID: e.rep1,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	page, err := e.store.Search(orgOnly, search.Input{Query: "rostock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].Type != "organization" {
		t.Fatalf("object RBAC leaked into search: %+v", page.Hits)
	}
	// Explicitly requesting only the denied type answers an empty page,
	// not an error — nothing to disclose.
	page, err = e.store.Search(orgOnly, search.Input{Query: "rostock", Types: []string{"person"}})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("denied-type search → %v %+v, want an empty page", err, page.Hits)
	}
}

func TestSearchRanksAcrossObjectTypes(t *testing.T) {
	e := setupSearch(t)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Heike Hamburg', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Hamburg Logistics GmbH', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO lead (id, workspace_id, company_name, email, source, captured_by) VALUES ($1, $2, 'Hamburg Freight', 'lead@hamburg.test', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, body, source, captured_by) VALUES ($1, $2, 'note', 'Hamburg visit', 'Met the Hamburg team at the Hamburg office in Hamburg', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Unrelated Munich', 'manual', 'human:x')`)

	page, err := e.store.Search(e.admin(), search.Input{Query: "hamburg"})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, hit := range page.Hits {
		types[hit.Type] = true
		if hit.Score <= 0 {
			t.Fatalf("hit without a rank: %+v", hit)
		}
		if strings.Contains(hit.Title, "Munich") {
			t.Fatalf("non-matching row surfaced: %+v", hit)
		}
	}
	for _, want := range []string{"person", "organization", "lead", "activity"} {
		if !types[want] {
			t.Errorf("no %s hit in %+v", want, page.Hits)
		}
	}
	// The activity mentions the term four times — repetition ranks it
	// above single-mention rows.
	if page.Hits[0].Type != "activity" {
		t.Errorf("rank order ignores term frequency: top hit %+v", page.Hits[0])
	}
}

func TestSearchHitsCarryTheCallersRowScope(t *testing.T) {
	e := setupSearch(t)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Scoped Bremen', $3, 'manual', 'human:x')`, e.rep3)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Shared Bremen', 'manual', 'human:x')`)

	// rep1 (team1, row_scope=team) must not see rep3's (team2) record —
	// but the ownerless row is workspace-shared.
	page, err := e.store.Search(e.asTeamRep(e.rep1, e.team1), search.Input{Query: "bremen"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].Title != "Shared Bremen" {
		t.Fatalf("row scope leaked into search: %+v", page.Hits)
	}
	// row_scope=all sees both.
	page, err = e.store.Search(e.admin(), search.Input{Query: "bremen"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 2 {
		t.Fatalf("unbounded scope sees %d, want 2", len(page.Hits))
	}
}

func TestSearchExcludesArchivedRows(t *testing.T) {
	e := setupSearch(t)
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by, archived_at) VALUES ($1, $2, 'Archived Kiel', 'manual', 'human:x', now())`)
	page, err := e.store.Search(e.admin(), search.Input{Query: "kiel"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 0 {
		t.Fatalf("archived row surfaced: %+v", page.Hits)
	}
}

func TestSearchRankedCursorWalksAllHitsOnce(t *testing.T) {
	e := setupSearch(t)
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := e.seed(t, fmt.Sprintf(`INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Dresden Contact %d', 'manual', 'human:x')`, i))
		want[id.String()] = false
	}
	got := 0
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		page, err := e.store.Search(e.admin(), search.Input{Query: "dresden", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range page.Hits {
			if seen, ok := want[hit.ID.String()]; !ok || seen {
				t.Fatalf("hit %s unknown or repeated", hit.ID)
			}
			want[hit.ID.String()] = true
			got++
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if got != 5 {
		t.Fatalf("cursor walk yielded %d of 5 hits", got)
	}
}

func TestSearchEmptyQueryIsAValidationError(t *testing.T) {
	e := setupSearch(t)
	_, err := e.store.Search(e.admin(), search.Input{Query: "   "})
	var bad *search.BadQueryError
	if err == nil || !errors.As(err, &bad) || !strings.Contains(bad.Reason, "required") {
		t.Fatalf("empty query → %v, want BadQueryError", err)
	}
}

// The PERF-3 posture, proven structurally instead of by a flaky
// wall-clock or planner assertion: every table the search union reads
// must define a GIN index over search_tsv, so the FTS predicate CAN
// scale past a sequential scan. (Which plan the optimizer picks at a
// given cardinality is its business; the index existing is ours.)
func TestSearchEveryBranchHasAGinIndex(t *testing.T) {
	e := setupSearch(t)
	for _, table := range []string{"person", "organization", "deal", "lead", "activity"} {
		var exists bool
		err := e.owner.QueryRow(context.Background(), `
			SELECT EXISTS (
			  SELECT 1 FROM pg_index i
			  JOIN pg_class idx ON idx.oid = i.indexrelid
			  JOIN pg_class tbl ON tbl.oid = i.indrelid
			  JOIN pg_am am ON am.oid = idx.relam
			  JOIN pg_attribute a ON a.attrelid = tbl.oid AND a.attnum = ANY(i.indkey)
			  WHERE tbl.relname = $1 AND am.amname = 'gin' AND a.attname = 'search_tsv')`,
			table).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s is searched but has no GIN index over search_tsv", table)
		}
	}
}
