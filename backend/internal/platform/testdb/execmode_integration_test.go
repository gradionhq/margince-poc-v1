// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestSharedPoolSurvivesATypeItsParametersName drives the failure that shipped
// green once already.
//
// The shared pool ran in pgx's cache_describe, which caches a statement's
// parameter type OIDs and assumes they keep meaning the same thing. They do not:
// a suite that drops and remigrates the schema recreates the pgvector extension
// with a fresh OID, and the next execution of a statement whose parameter the
// server typed as vector — search's embedding upsert binds $5::vector — draws
// XX000 "cache lookup failed for type N", in whichever suite ran next.
//
// The soundness of the mode was argued in a comment and gated nowhere, so the
// regression was invisible until it reached CI in an unrelated suite. This
// reproduces the mechanism in nine lines and without pgvector: declare a type,
// run a statement whose parameter the server types as that type, recreate the
// type underneath the pool, and run the same statement again. Under
// cache_describe or cache_statement the second run fails; under describe_exec it
// re-describes and passes.
func TestSharedPoolSurvivesATypeItsParametersName(t *testing.T) {
	pool := sharedAppPool(t)
	owner := probeOwnerConn(t)
	ctx := context.Background()

	// Owned by the owner role and named for this test, so it cannot collide with
	// a record table or with another package's clone.
	const ddl = `execmode_probe`
	if _, err := owner.Exec(ctx, `DROP TYPE IF EXISTS `+ddl+`; CREATE TYPE `+ddl+` AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("declaring the probe type: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TYPE IF EXISTS `+ddl); err != nil {
			t.Errorf("dropping the probe type: %v", err)
		}
	})

	// The cast is what makes the server type the parameter as the probe type,
	// which is what a caching mode would then hold an OID for.
	probe := func(t *testing.T, when string) {
		t.Helper()
		var got string
		if err := pool.QueryRow(ctx, `SELECT ($1::`+ddl+`)::text`, "a").Scan(&got); err != nil {
			t.Fatalf("binding the probe type %s recreating it: %v — the pool is caching parameter OIDs across a schema change, so a suite that remigrates will fail a later, unrelated one",
				when, err)
		}
		if got != "a" {
			t.Fatalf("the probe round-tripped %q, want \"a\"", got)
		}
	}
	probe(t, "before")

	// Same name, new OID: exactly what dropping and remigrating the schema does to
	// an extension's types.
	if _, err := owner.Exec(ctx, `DROP TYPE `+ddl+`; CREATE TYPE `+ddl+` AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("recreating the probe type: %v", err)
	}
	probe(t, "after")
}

// probeOwnerConn opens an owner connection for the DDL above. Its own rather than
// the one reset_integration_test.go has, which is internal to the package while
// this test is external — external because it asserts what a CALLER of the shared
// pool sees, which is the thing that broke.
func probeOwnerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	return conn
}
