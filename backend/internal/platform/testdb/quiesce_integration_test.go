// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The quiesce observation had three mechanism bugs before it had a test, and
// every one of them would have died here in a second: it sampled once and read a
// finishing goroutine as a leak, and it then "waited" by acquiring, which pgxpool
// satisfies from idle or by dialling — so it could only fire when nine of sixteen
// connections were leaked at once. A gate whose own mechanism is untested is a
// comment with a cost.
//
// The leak it cannot see per test — a poll loop holding nothing between
// queries — is #770, and deliberately has no arm here: a test asserting a known
// gap passes for the wrong reason.
//
// Internal to the package because poolQuiesced is not the caller-facing surface —
// AssertPoolsQuiesced is, and what needs pinning is the observation underneath it.

const quiesceTestGrace = 300 * time.Millisecond

// TestQuiescedPoolReportsQuiet is the negative control: without it, a gate that
// never reports quiet would look just as green as one that works.
func TestQuiescedPoolReportsQuiet(t *testing.T) {
	pool := quiesceProbePool(t)
	// Use it first, so the pool has an idle connection and a non-zero acquire
	// count. Observing a pool that has never been touched would prove nothing
	// about telling an idle connection from a held one.
	if _, err := pool.Exec(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("warming the probe pool: %v", err)
	}
	if outstanding := poolQuiesced(pool, quiesceTestGrace, 10*time.Millisecond); outstanding != 0 {
		t.Errorf("a pool nobody is using reported %d connection(s) checked out — the gate would fail every test in the lane", outstanding)
	}
}

// TestAHeldConnectionIsNotQuiet is the arm the acquire-based version could not
// pass: ONE connection checked out, fifteen slots free.
func TestAHeldConnectionIsNotQuiet(t *testing.T) {
	pool := quiesceProbePool(t)
	held, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("holding a connection: %v", err)
	}
	defer held.Release()

	if outstanding := poolQuiesced(pool, quiesceTestGrace, 10*time.Millisecond); outstanding != 1 {
		t.Errorf("one connection was checked out for the whole window and the gate saw %d — a straggler holds one or two, never MaxConns, so this is the only count that matters",
			outstanding)
	}
}

// quiesceProbePool is a pool of this package's own, never the shared one: these
// tests deliberately leave a connection checked out and drive traffic, and the
// shared pool is what AssertPoolsQuiesced inspects at the end of every OTHER test
// in the process.
func quiesceProbePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if appDSN == "" || ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	owner, err := pgx.Connect(context.Background(), ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := EnsureSchema(context.Background(), owner); err != nil {
		t.Fatalf("migrating the test schema: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("opening the probe pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
