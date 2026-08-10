// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestPoolRefusesBeforeTheSchemaIsMigrated drives the one branch that keeps a
// pooled connection from predating the migration's DROP SCHEMA.
//
// It is an internal test because the state it needs to reach is internal:
// EnsureSchema's sync.Once has already fired by the time any test in this binary
// runs, so the unmigrated case is unreachable from outside. Clearing the flag for
// one call is safe — tests within a package run serially under the lane's -p 1
// and nothing here calls t.Parallel — and Pool returns before it touches the pool
// map, so the process keeps whatever pools it already had.
//
// Without this, deleting the check leaves every suite green while a session from
// a too-early pool blocks the migration's DROP SCHEMA — see ErrSchemaNotReady for
// why that costs the package rather than the one test.
func TestPoolRefusesBeforeTheSchemaIsMigrated(t *testing.T) {
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	was := schemaReady.Load()
	t.Cleanup(func() { schemaReady.Store(was) })

	schemaReady.Store(false)
	pool, err := Pool(context.Background(), appDSN)
	if pool != nil {
		t.Error("Pool handed out a pool for an unmigrated database")
	}
	if !errors.Is(err, ErrSchemaNotReady) {
		t.Fatalf("Pool on an unmigrated database returned %v, want ErrSchemaNotReady", err)
	}
}
