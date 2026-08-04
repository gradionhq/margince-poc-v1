// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// run() defers lanes.join() BEFORE it checks startEventLanes' error, because a
// boot step failing after a lane has started must still cancel and wait for it —
// the deferred closeBus and pool.Close would otherwise run under a live
// subscriber. That path needs a real bus and pool: with nil dependencies the
// lanes short-circuit and never start, so nothing is left running to join.

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
)

func TestABootFailureAfterALaneStartedStillJoinsIt(t *testing.T) {
	pool := workerTestPool(t)
	rdb := budgettest.Client(t)

	// announced takes only the phases' own Fprintln calls, which this goroutine
	// makes synchronously inside startEventLanes. The LANES get a discarding
	// logger: a subscriber that logs a bus hiccup would otherwise write this
	// buffer from its own goroutine while the assertion below reads it.
	var announced bytes.Buffer
	laneLog := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The lane goroutines are the only ones this call adds, so the count is how
	// the test knows something is actually running to be joined.
	beforeLanes := runtime.NumGoroutine()

	// The projection lanes start unconditionally; the webhook lane then refuses a
	// malformed signing key. So the failure lands AFTER goroutines exist, which is
	// the only interesting shape — a failure before any lane starts is trivially
	// safe to return from.
	lanes, err := startEventLanes(t.Context(), workerConfig{webhookKey: "not-a-valid-signing-key"},
		pool, rdb, compose.ModelPath{}, laneLog, &announced)
	if err == nil {
		t.Fatal("startEventLanes accepted a malformed webhook signing key — this test needs it to fail AFTER a lane started")
	}
	if lanes.background == nil || lanes.stop == nil {
		t.Fatal("a failing startEventLanes handed back a value join() cannot use; run() defers that join before it sees the error")
	}
	// Without a live goroutine there is nothing to cancel and join() would return
	// whatever it does, so the rest of this test would prove nothing. The phase
	// banner alone is not enough: it is printed before the goroutine is launched,
	// and a subscriber that failed to attach to its group would already be gone.
	if got := runtime.NumGoroutine(); got <= beforeLanes {
		t.Fatalf("goroutines %d → %d: no lane is running, so this test cannot prove join() ends one (announced: %q)",
			beforeLanes, got, announced.String())
	}
	if !strings.Contains(announced.String(), "interaction edges") {
		t.Errorf("the projection lane did not announce itself, so an operator reading the boot log cannot "+
			"tell it started: %q", announced.String())
	}

	// The assertion is that this RETURNS. A lane join() cannot cancel would hang
	// here until the package timeout, which is what a missing cancel looks like —
	// no sleep and no clock of our own.
	lanes.join()

	// join() must not close what it was handed: run() closes the bus and the pool
	// on defers that fire after it, and they cannot fire twice.
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Errorf("join() closed the bus client it was given: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Errorf("join() closed the pool it was given: %v", err)
	}
}

// workerTestPool opens the app-role pool the lanes consume through. It fails
// loudly rather than skipping — a boot-ordering gate that quietly does not run
// looks exactly like one that passed.
func workerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("opening the app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
