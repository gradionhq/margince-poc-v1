// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// The one way this package waits for a backend that is provably blocked. Both
// contention suites here — the bundle decision racing a held row, the staging
// racing a held identity lock — ask the database the same question in the same
// shape, and they used to disagree about how to give up on it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// probeBudget bounds the wait for a backend that never blocks.
//
// It is a DURATION, and it used to be a count of 20 000 probes. A count is not a
// budget: it is a race between how fast probe round trips complete and how fast
// the racer reaches its lock, and the lane's own concurrency slows BOTH, so a
// count generous on an idle machine is not generous on a loaded one. A duration
// means the same thing on every machine.
//
// Generous enough that only a genuine miss trips it, short enough that the miss
// reports itself rather than running into the package timeout, where it would
// read as a hung suite instead of a stated fact. That ceiling is arithmetic
// rather than taste: five call sites in this package can each spend this budget,
// against the lane's 600s per-package timeout (INTEGRATION_TIMEOUT). At 90s a run
// in which every one of them misses spends 450s and still reports what it found.
// Raise this number and that sum moves with it.
const probeBudget = 90 * time.Second

// probeInterval paces the poll, so the observer is not competing for the very
// resource it is waiting on.
//
// Unpaced, these loops issued round trips as fast as the server would answer
// them, and the pg_stat_activity probe's pg_blocking_pids is documented as
// needing exclusive access to the lock manager's shared state for a short time —
// the same state the racer must acquire to register its own lock wait. A watcher
// holding that thousands of times a second is not a neutral observer of
// contention; on a loaded runner it is part of it.
//
// 25ms is far finer than anything here needs: every block these tests wait for
// persists until the holding transaction ends, so it cannot be missed between
// ticks, and a racer that FINISHES is seen at once through done rather than on a
// tick.
const probeInterval = 25 * time.Millisecond

// waitForBlockedBackend polls look until it reports the wait the caller needs,
// the racer finishes without ever blocking, or the budget runs out. Both of the
// latter are failures, and each caller supplies what they mean in its own terms:
// a probe that gave up must say what the run failed to prove, never pass having
// proved nothing.
func waitForBlockedBackend(
	t *testing.T,
	done <-chan struct{},
	finishedEarly, missed string,
	look func(context.Context) (bool, error),
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()
	pace := time.NewTicker(probeInterval)
	defer pace.Stop()
	for {
		blocked, err := look(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			t.Fatal(racerMessage(done, finishedEarly, missed))
		case err != nil:
			t.Fatalf("probing for a blocked backend: %v", err)
		}
		if blocked {
			return
		}
		// One select for all three answers: the racer finished, the budget ran
		// out, or it is time to look again.
		select {
		case <-done:
			t.Fatal(finishedEarly)
		case <-ctx.Done():
			t.Fatal(racerMessage(done, finishedEarly, missed))
		case <-pace.C:
		}
	}
}

// racerMessage picks which failure actually happened when the budget expires.
//
// When the racer finishes AS the budget runs out, both channels are ready and
// select picks between them arbitrarily — so the timeout branch can be taken
// while a finished racer sits unread, and the run would be reported as "nothing
// ever blocked" when what really happened is that the racer never blocked at
// all. Those are different diagnoses and the second one is the useful one.
func racerMessage(done <-chan struct{}, finishedEarly, missed string) string {
	select {
	case <-done:
		return finishedEarly
	default:
		return missed
	}
}

// The row-lock probe sees a backend that dials AFTER it starts looking.
//
// Four bundle contention tests rest on that, and none of them assert it, because
// on a machine with a warm pool it holds by accident: the racer's connection
// already exists when the probe takes its first look, so the transaction-scoped
// statistics snapshot happens to contain it. The competing transaction here is
// open on the SAME connection the probe uses, so that snapshot is frozen for the
// whole race — and under the lane's concurrency the pool has no idle connection
// to hand, dials one mid-race, and a probe trusting that snapshot never sees it.
//
// So the ordering is PINNED rather than raced for: the first look is taken while
// no racer exists, and the racer then arrives on a connection dialled
// afterwards. That makes this fail on a laptop against the bug it describes,
// instead of only on a loaded runner.
func TestTheRowLockProbeSeesABackendThatDialsAfterItsFirstLook(t *testing.T) {
	e := setupStaging(t)
	ctx := context.Background()
	competing := e.competingTx(t)
	blocker := backendPID(t, competing)
	// Any lock this connection can hold and another can queue on proves the
	// probe; an advisory one needs no seeded row to contend over.
	const contested = 8_712_004
	if _, err := competing.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, contested); err != nil {
		t.Fatalf("taking the contested lock: %v", err)
	}

	// The look that used to freeze this connection's view of who is connected.
	var queued bool
	if err := e.owner.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_stat_activity a
		  WHERE $1 = ANY (pg_blocking_pids(a.pid)))`, blocker).Scan(&queued); err != nil {
		t.Fatalf("the probe's first look: %v", err)
	}
	if queued {
		t.Fatal("something was already queued on the competing transaction before the racer " +
			"existed — this run cannot tell a working probe from a blind one")
	}

	racer := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		racer <- queueOnAdvisoryLockFromAFreshConnection(ctx, contested)
	}()

	waitForRowLockWaiter(t, e, blocker, done)

	// Let the racer through and account for it. A goroutine still holding a
	// connection when the test returns outlives the *testing.T it reports
	// through, and panics whichever package it lands in.
	if err := competing.Rollback(ctx); err != nil {
		t.Fatalf("releasing the contested lock: %v", err)
	}
	if err := <-racer; err != nil {
		t.Fatalf("the racer never got the lock the holder released: %v", err)
	}
}

// queueOnAdvisoryLockFromAFreshConnection queues for the contested lock from a
// backend that did not exist when this call began, and returns once it holds it
// — which is only after the holder lets go. Its own connection is the point: a
// pooled one may pre-date the probe's first look, and then it proves nothing
// about a racer that does not.
func queueOnAdvisoryLockFromAFreshConnection(ctx context.Context, key int) (err error) {
	conn, err := pgx.Connect(ctx, os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		return fmt.Errorf("dialling the racer's own connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close(context.Background())) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("opening the racer's transaction: %w", err)
	}
	// The lock is held for the transaction, so releasing it IS the rollback.
	defer func() {
		if rollback := tx.Rollback(context.Background()); !errors.Is(rollback, pgx.ErrTxClosed) {
			err = errors.Join(err, rollback)
		}
	}()
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key)
	return err
}
