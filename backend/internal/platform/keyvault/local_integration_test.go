// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package keyvault_test

// GetOn's whole reason for existing is WHICH CONNECTION the read lands on, and
// that is not a claim the memory provider can carry: its GetOn ignores the
// querier entirely, so a local provider quietly regressed back to `v.pool`
// would pass every test in this package's unit suite while reintroducing the
// deadlock the method was added to remove — a caller inside a transaction
// holding one pooled connection and waiting for a second.
//
// So this exercises the local provider over a real pool, and pins the property
// behaviourally rather than by inspection: with the pool narrowed to ONE
// connection and that connection held by an open transaction, a Get must fail
// to acquire and a GetOn through the transaction must answer. A GetOn that took
// its own connection cannot pass this.

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// singleConnPool is the fixture the whole test rests on: a pool that can hand
// out exactly one connection, so "needs a second one" and "deadlocks" are the
// same observable event and the test does not have to race a real burst.
func singleConnPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ownerDSN, appDSN := os.Getenv("MARGINCE_TEST_DSN"), os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := t.Context()

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
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatalf("resetting test data: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatalf("parsing the app DSN: %v", err)
	}
	cfg.MaxConns, cfg.MinConns = 1, 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("opening the single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func rootKey(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("minting a root key: %v", err)
	}
	return raw
}

func TestLocalGetOnReadsThroughTheCallersTransaction(t *testing.T) {
	pool := singleConnPool(t)
	vault, err := keyvault.New(keyvault.Config{RootKey: rootKey(t), Pool: pool})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	ctx := t.Context()
	ws := ids.From[ids.WorkspaceKind](ids.NewV7())

	ref, err := vault.Put(ctx, ws, []byte("s3cr3t"))
	if err != nil {
		t.Fatalf("sealing the secret: %v", err)
	}

	// The pool's one connection, held open — the position every extsecrets read
	// is in when it resolves a ref.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the transaction: %v", err)
	}
	defer func() {
		//craft:ignore swallowed-errors the transaction is read-only and being abandoned
		_ = tx.Rollback(context.Background())
	}()

	got, err := vault.GetOn(ctx, tx, ws, ref)
	if err != nil {
		t.Fatalf("GetOn through the caller's transaction: %v — it must not need a connection of its own", err)
	}
	if string(got) != "s3cr3t" {
		t.Fatalf("GetOn returned %q", got)
	}

	// And the control, which is what makes the assertion above mean something:
	// the same read through Get asks the pool for a SECOND connection, and the
	// pool has none to give while the transaction holds its one. It blocks
	// rather than failing, so the deadline is the observation.
	blocked, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := vault.Get(blocked, ws, ref); err == nil {
		t.Fatal("Get succeeded while the pool's only connection was held by an open transaction — " +
			"it no longer takes a connection of its own, and GetOn's reason for existing is gone")
	}
}
