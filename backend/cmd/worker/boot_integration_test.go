// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// run() defers lanes.join() BEFORE it checks startEventLanes' error, because a
// boot step failing after a lane has started must still cancel and wait for it —
// the deferred closeBus and pool.Close would otherwise run under a live
// subscriber. That path needs a real bus and pool: with nil dependencies the
// lanes short-circuit and never start, so nothing is left running to join. Hence
// this lane rather than a unit test.

import (
	"bytes"
	"log/slog"
	"os"
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

	// The projection lanes start unconditionally; the webhook lane then refuses a
	// malformed signing key. So the failure lands AFTER goroutines exist, which is
	// the only interesting shape — a failure before any lane starts is trivially
	// safe to return from.
	var announced bytes.Buffer
	lanes, err := startEventLanes(t.Context(), workerConfig{webhookKey: "not-a-valid-signing-key"},
		pool, rdb, compose.ModelPath{}, slog.New(slog.NewTextHandler(&announced, nil)), &announced)
	if err == nil {
		t.Fatal("startEventLanes accepted a malformed webhook signing key — this test needs it to fail AFTER a lane started")
	}
	if lanes.background == nil || lanes.stop == nil {
		t.Fatal("a failing startEventLanes handed back a value join() cannot use; run() defers that join before it sees the error")
	}
	// Without this the test could pass vacuously: join() on a set that started
	// nothing returns immediately, proving neither the cancel nor the wait.
	if !strings.Contains(announced.String(), "interaction edges") {
		t.Fatalf("no lane announced itself before the failure, so there is nothing to join: %q", announced.String())
	}

	// The assertion is that this RETURNS. A lane join() cannot cancel would hang
	// here until the package timeout, which is what a missing cancel looks like —
	// no sleep and no clock of our own.
	lanes.join()

	// The lanes are done, so what run() closes next is safe to close: prove the
	// bus and the pool are still open at this point, which is the ordering the
	// deferred closes depend on.
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Errorf("the bus was closed before the lanes were joined: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Errorf("the pool was closed before the lanes were joined: %v", err)
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
