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

// TestSharedPoolSurvivesATypeItsParametersName pins the shared pool's exec mode to
// one that no schema change can stale.
//
// A caching mode holds a statement's parameter type OIDs and assumes they keep
// meaning the same thing. They do not: a suite that drops and remigrates the
// schema recreates the pgvector extension with a fresh OID, and the next execution
// of a statement whose parameter the server typed as vector — search's embedding
// upsert binds $5::vector — draws XX000 "cache lookup failed for type N", in
// whichever suite ran next rather than the one that remigrated.
//
// That soundness used to be argued in a comment and gated nowhere, so the mistake
// was invisible until CI failed in an unrelated suite. This reproduces the
// mechanism without pgvector and without a migration: declare a type, bind a
// parameter the server types as it, recreate the type underneath the connection,
// bind again. Verified by mutation — under cache_describe the second bind fails
// with the real XX000. cache_statement is documented to fail the same class on its
// named plans and is not separately exercised here.
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

	// ONE connection for both probes, because pgx caches per connection. Through
	// pool.QueryRow each probe is its own acquire, and a second probe served a
	// different (or newly dialled) connection meets an empty cache and passes
	// whatever the mode is — so a mutation test would be measuring puddle's idle
	// ordering rather than this assertion. Pinning the connection makes the subject
	// ("a connection may outlive a schema change") literally what runs.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring the probe connection: %v", err)
	}
	defer conn.Release()

	// The cast is what makes the server type the parameter as the probe type,
	// which is what a caching mode then holds an OID for.
	bind := func() (string, error) {
		var got string
		err := conn.QueryRow(ctx, `SELECT ($1::`+ddl+`)::text`, "a").Scan(&got)
		return got, err
	}

	// Before anything is recreated, a failure is the probe's own — no cache can be
	// stale yet — so it must not be reported as a caching fault.
	got, err := bind()
	if err != nil {
		t.Fatalf("the probe cannot bind its own type: %v — the probe is broken, not the exec mode", err)
	}
	if got != "a" {
		t.Fatalf("the probe round-tripped %q, want \"a\"", got)
	}

	// Same name, new OID: what dropping and remigrating the schema does to an
	// extension's types.
	if _, err := owner.Exec(ctx, `DROP TYPE `+ddl+`; CREATE TYPE `+ddl+` AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("recreating the probe type: %v", err)
	}
	got, err = bind()
	if err != nil {
		t.Fatalf("binding the probe type after recreating it: %v — this connection is caching parameter OIDs across a schema change, so a suite that remigrates will fail a later, unrelated one", err)
	}
	if got != "a" {
		t.Fatalf("after recreating the type the probe round-tripped %q, want \"a\"", got)
	}
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
