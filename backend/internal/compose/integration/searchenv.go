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
)

// SearchEnv is the second migrated-database fixture in this package, lighter
// than Env: one workspace, two reps in two teams, and a search store over the
// RLS-bound pool. Forty suites ride it, most of them — capture, the connectors,
// leadscore, export — for the database handles rather than the store, which is
// why it lives here rather than with the search suite that named it.
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
