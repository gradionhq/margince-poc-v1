// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The shared pool is the one every RLS and authz suite reads through, so what it
// must not become is a second pool constructor that drifts from the one cmd/api
// opens. It builds through database.NewPool and names its differences as DSN
// parameters; these tests are what keeps that true, because the ways it can stop
// being true are all silent.

// TestSharedPoolKeepsTheProductionConnectionSetup checks the two halves of
// database.NewPool's per-connection setup that a hand-built pgxpool config
// silently drops — as an earlier revision of this pool did.
//
// The two halves fail differently, and only one of them is the detector.
// Mutation-tested by rebuilding the pool from a bare pgxpool.ParseConfig: the jit
// assertion goes red, the typed-id slice bind stays green. That is not a weak
// assertion, it is what the exec mode does — the pool runs describe_exec, where
// the parameter OID always arrives from the server, so pgx never has to consult
// the registered type to encode []ids.PersonID. The bind is here to pin the idiom
// itself: pgx's exec and simple_protocol modes infer parameter types from the Go
// values instead, and would fail on this slice, so a future change of mode meets
// this test rather than a record store. jit is the half that catches the
// construction regressing.
func TestSharedPoolKeepsTheProductionConnectionSetup(t *testing.T) {
	pool := sharedAppPool(t)
	ctx := context.Background()

	// The EFFECTIVE mode, not the one testPoolParams asks for. withTestPoolParams
	// lets a DSN that already names a parameter keep its own value, and
	// scripts/lib-testdb.sh copies the base DSN's query string verbatim into every
	// clone — so an exported MARGINCE_TEST_APP_DSN carrying a caching mode
	// reinstates the unsoundness this pool exists to avoid, without touching any
	// Go file. Asserted rather than trusted for that reason.
	if got := pool.Config().ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeDescribeExec {
		t.Errorf("the shared pool runs exec mode %v, want describe_exec — a caching mode lets a connection outlive the schema it cached against, and the DSN wins over testPoolParams, so check MARGINCE_TEST_APP_DSN",
			got)
	}

	want := ids.New[ids.PersonKind]()
	other := ids.New[ids.PersonKind]()
	var matched int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT unnest(ARRAY[$1::uuid, $2::uuid]) AS id) rows WHERE rows.id = ANY($3)`,
		want, other, []ids.PersonID{want, other}).Scan(&matched); err != nil {
		t.Fatalf("binding a typed-id slice through the shared pool: %v — ANY($n) over typed ids is used throughout the record stores, so the lane can no longer exercise them", err)
	}
	if matched != 2 {
		t.Fatalf("the ANY bind matched %d rows, want 2", matched)
	}

	// jit=off rides the same construction. Reading it back is cheaper than
	// re-measuring the plan time that motivated it, and fails for the same
	// reason the bind above would.
	var jit string
	if err := pool.QueryRow(ctx, `SHOW jit`).Scan(&jit); err != nil {
		t.Fatalf("reading jit: %v", err)
	}
	if jit != "off" {
		t.Errorf("the shared pool has jit=%s; database.NewPool turns it off, and the lane's row-scope predicates are why", jit)
	}
}

// TestSharedPoolIsOneObjectPerDSN pins the sharing itself: two asks for the same
// DSN must be the same pool, or every fixture is back to a pool per test and the
// connection cost this package exists to remove comes back unannounced.
func TestSharedPoolIsOneObjectPerDSN(t *testing.T) {
	if first, second := sharedAppPool(t), sharedAppPool(t); first != second {
		t.Error("two asks for the same DSN returned different pools — the pool is no longer shared per process")
	}
}

// The schema-not-ready refusal is driven from
// poolready_integration_test.go, which is internal to the package because the
// unmigrated state is only reachable through the flag EnsureSchema sets.

// sharedAppPool migrates this process's database and returns the shared app-role
// pool, which is the order Pool enforces.
func sharedAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatalf("migrating the test schema: %v", err)
	}
	pool, err := testdb.Pool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening the shared pool: %v", err)
	}
	return pool
}
