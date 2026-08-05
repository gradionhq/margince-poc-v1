// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobs_test

// Real-Postgres lane for the reset's queue control: pause/resume round-trips
// through river_queue, and the purge's workspace filter.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestPurgeWorkspaceKeepsAnotherWorkspacesRows(t *testing.T) {
	ctx, pool := quiesceTestPool(t)
	mine, theirs := ids.NewV7(), ids.NewV7()
	insertJobRow(t, ctx, pool, "reset.probe", mine)
	insertJobRow(t, ctx, pool, "reset.probe", theirs)

	deleted, err := jobs.PurgeWorkspace(ctx, pool, mine)
	if err != nil {
		t.Fatalf("PurgeWorkspace: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if got := countJobRows(t, ctx, pool, theirs); got != 1 {
		t.Errorf("another workspace lost %d rows; a reset must never cross the tenant boundary", 1-got)
	}
}

func TestPurgeWorkspaceRemovesFleetDispatchers(t *testing.T) {
	ctx, pool := quiesceTestPool(t)
	ws := ids.NewV7()
	insertDispatcherRow(t, ctx, pool, "reset.dispatcher") // args with no workspace_id key

	deleted, err := jobs.PurgeWorkspace(ctx, pool, ws)
	if err != nil {
		t.Fatalf("PurgeWorkspace: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want the NULL-args dispatcher row removed; periodic ticks re-insert it", deleted)
	}
}

func TestQuiesceReportsNotDrainedWhileAJobRuns(t *testing.T) {
	ctx, pool := quiesceTestPool(t)
	insertRunningJobRow(t, ctx, pool, "reset.stuck")

	q := jobs.Quiescer{
		Runner:   quiesceTestRunner(t, pool),
		Pool:     pool,
		Timeout:  50 * time.Millisecond,
		Interval: 5 * time.Millisecond,
		Now:      time.Now,
	}
	drained, err := q.Quiesce(ctx)
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if drained {
		t.Error("drained = true with a running row present; the drain must report the timeout, not hide it")
	}
	if err := q.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
}

func TestQuiesceDrainsWhenNothingRuns(t *testing.T) {
	ctx, pool := quiesceTestPool(t)
	q := jobs.Quiescer{
		Runner: quiesceTestRunner(t, pool), Pool: pool,
		Timeout: time.Second, Interval: 5 * time.Millisecond, Now: time.Now,
	}
	drained, err := q.Quiesce(ctx)
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if !drained {
		t.Error("drained = false on an idle queue set")
	}
}

// quiesceTestPool gives the test a migrated, empty database carrying the
// River schema, on the app-role pool — the same role QueuePause/QueueResume
// and the purge's DELETE run as. Mirrors migratedAppPool
// (jobs_integration_test.go) minus the worker wiring this file never starts.
func quiesceTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}

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

	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	if err := testdb.EnsureRiverSchema(ctx, ownerPool, jobs.Migrate); err != nil {
		t.Fatal(err)
	}

	appPool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	return ctx, appPool
}

// quiesceTestRunner wraps the pool in an insert-only Runner: Quiesce and
// Resume only ever call QueuePause/QueueResume on the client, which needs no
// configured queues or workers — an inserter is enough to reach them.
func quiesceTestRunner(t *testing.T, pool *pgxpool.Pool) *jobs.Runner {
	t.Helper()
	r, err := jobs.NewInserter(pool, quietLogger())
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	return r
}

// insertJobRow seeds one available, tenant-scoped row: exactly what a
// workspace's own queued work looks like on the wire.
func insertJobRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, ws ids.UUID) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"workspace_id": ws.String()})
	if err != nil {
		t.Fatalf("encoding fixture args: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job (state, kind, queue, args, tags, errors, max_attempts, attempt, created_at, scheduled_at)
		VALUES ('available', $1, 'default', $2::jsonb, '{}'::varchar(255)[], '{}'::jsonb[], 3, 0, now(), now())`,
		kind, args); err != nil {
		t.Fatalf("seeding a %s row: %v", kind, err)
	}
}

// insertDispatcherRow seeds one available row with no workspace_id key at
// all — a fleet dispatcher's args, not a malformed tenant row.
func insertDispatcherRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job (state, kind, queue, args, tags, errors, max_attempts, attempt, created_at, scheduled_at)
		VALUES ('available', $1, 'default', '{}'::jsonb, '{}'::varchar(255)[], '{}'::jsonb[], 3, 0, now(), now())`,
		kind); err != nil {
		t.Fatalf("seeding a %s dispatcher row: %v", kind, err)
	}
}

// insertRunningJobRow seeds a row already claimed by a worker, written
// directly rather than through a real worker loop: the drain has to see a
// running row without one ever actually executing.
func insertRunningJobRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO river_job (state, kind, queue, args, tags, errors, max_attempts, attempt, created_at, scheduled_at, attempted_at)
		VALUES ('running', $1, 'default', '{}'::jsonb, '{}'::varchar(255)[], '{}'::jsonb[], 3, 1, now(), now(), now())`,
		kind); err != nil {
		t.Fatalf("seeding a running %s row: %v", kind, err)
	}
}

// countJobRows reports how many river_job rows carry the given workspace —
// the read PurgeWorkspace's tenant-boundary assertion checks.
func countJobRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws ids.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM river_job WHERE args->>'workspace_id' = $1`,
		ws.String()).Scan(&n); err != nil {
		t.Fatalf("counting rows for workspace: %v", err)
	}
	return n
}
