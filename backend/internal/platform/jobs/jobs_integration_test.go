// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobs_test

// Real-Postgres lane for the River lifecycle: the schema applies, the
// runtime role can reach it, and a client with no work boots and drains
// cleanly. The behavior-preserving swap of the actual worker loops is
// proven in internal/compose (jobs_integration_test.go).

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// noopWorker lets the chassis start with a registered worker (River
// requires at least one); it does nothing — the point is the lifecycle,
// not the work.
type noopArgs struct{}

func (noopArgs) Kind() string { return "noop" }

type noopWorker struct {
	river.WorkerDefaults[noopArgs]
}

func (noopWorker) Work(context.Context, *river.Job[noopArgs]) error { return nil }

// migratedAppPool gives the test a migrated, empty database carrying the River
// schema, and returns a runner plus the underlying pool on the runtime app role
// — proving the grants in jobs.Migrate actually let the app role reach River's
// tables. The pool is returned alongside the runner so callers can open their
// own transactions (e.g. to exercise EnqueueTx*).
//
// The schema is migrated once per test process (testdb.EnsureSchema) and only the
// data is reset between tests (testdb.Reset). It used to DROP SCHEMA and re-run
// every embedded migration on each of this package's 16 tests, which cost ~3s a
// test — the per-test migrate the module-wide gate in backend/
// integrationmigrateonce_test.go exists to forbid, and which that gate could not
// see while it walked internal/compose/integration alone. EnsureSchema opens with
// the same DROP SCHEMA + GRANT USAGE TO margince_app, so the app-role reach this
// suite asserts is established exactly as before.
func migratedAppPool(t *testing.T) (*jobs.Runner, *pgxpool.Pool) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
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

	// River schema is applied on the owner pool, exactly as cmd/migrate does.
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	if err := testdb.EnsureRiverSchema(ctx, ownerPool, jobs.Migrate); err != nil {
		t.Fatal(err)
	}

	// The runner runs on the app role — the same role the worker uses.
	appPool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	workers := river.NewWorkers()
	river.AddWorker(workers, &noopWorker{})
	r, err := jobs.New(appPool, jobs.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	}, quietLogger())
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	return r, appPool
}

// riverLifecycleBudget bounds the wait for the fixture job to be claimed
// and finished. It is a failure guard, not a pace: the test proceeds the
// instant River reports the completion, and only a client that never got
// there spends the budget.
const riverLifecycleBudget = 30 * time.Second

// TestRiversOwnMaintenanceWritesNoJobRowsOfItsOwn pins the belief the
// unrecognised-kind metric rests on.
//
// That metric reports rows whose kind api/jobs.yaml does not declare, and it
// is readable as a signal only if River's own upkeep is never one of them:
// the rescuer, the cleaner, the scheduler and the leader election are
// SERVICES inside the client, running on timers over their own tables, not
// work enqueued into river_job. Reasonable, and cheaper to verify than to
// leave a reader to trust — a single housekeeping row of River's own would
// put a permanent series on a family whose whole purpose is to be absent.
//
// A whole client lifecycle, not just a boot: election, maintenance startup,
// one job claimed, worked and completed, then a graceful drain. The only
// kind that may have reached the table afterwards is the one enqueued here.
func TestRiversOwnMaintenanceWritesNoJobRowsOfItsOwn(t *testing.T) {
	r, pool := migratedAppPool(t)
	ctx := t.Context()

	// Subscribed before Start so the completion cannot be missed, and waited
	// on rather than slept through: what matters is that the client got far
	// enough to claim and finish work, not that an interval elapsed.
	completed, unsubscribe := r.SubscribeCompleted()
	defer unsubscribe()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Enqueue(ctx, noopArgs{}, nil); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, riverLifecycleBudget)
	defer cancelWait()
	select {
	case <-completed:
	case <-waitCtx.Done():
		t.Fatalf("the fixture job was never worked: %v — the lifecycle this test is about "+
			"did not happen, so its finding would be vacuous", waitCtx.Err())
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	cursor, err := pool.Query(ctx, `SELECT DISTINCT kind FROM river_job ORDER BY kind`)
	if err != nil {
		t.Fatalf("reading the kinds that reached river_job: %v", err)
	}
	defer cursor.Close()
	var kinds []string
	for cursor.Next() {
		var kind string
		if err := cursor.Scan(&kind); err != nil {
			t.Fatalf("scanning a kind: %v", err)
		}
		kinds = append(kinds, kind)
	}
	if err := cursor.Err(); err != nil {
		t.Fatalf("reading the kinds that reached river_job: %v", err)
	}

	want := []string{noopArgs{}.Kind()}
	if !slices.Equal(kinds, want) {
		t.Errorf("river_job holds kinds %v, want only %v — River enqueued housekeeping of its "+
			"own, which the unrecognised-kind metric would report forever as work of a kind "+
			"the contract does not declare", kinds, want)
	}
}

func TestRunnerStartsAndStopsCleanlyAsAppRole(t *testing.T) {
	r, _ := migratedAppPool(t)
	ctx := t.Context()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestEnqueueTxUniqueSurfacesDedupeOutcome proves the confirm endpoint's
// premise: enqueuing the same unique job twice in the same transaction
// tells the two outcomes apart without an error — the first insert lands,
// the second is River's own dedupe, not a failure.
func TestEnqueueTxUniqueSurfacesDedupeOutcome(t *testing.T) {
	r, pool := migratedAppPool(t)
	ctx := t.Context()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		// Committed below on the success path; Rollback after a successful
		// Commit reports ErrTxClosed, which is expected, not swallowed.
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rolling back tx: %v", err)
		}
	}()

	opts := &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}

	inserted, err := r.EnqueueTxUnique(ctx, tx, noopArgs{}, opts)
	if err != nil {
		t.Fatalf("first EnqueueTxUnique: %v", err)
	}
	if !inserted {
		t.Fatal("first enqueue: want inserted=true, got false")
	}

	inserted, err = r.EnqueueTxUnique(ctx, tx, noopArgs{}, opts)
	if err != nil {
		t.Fatalf("second EnqueueTxUnique: %v", err)
	}
	if inserted {
		t.Fatal("second enqueue: want inserted=false (deduped), got true")
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
}
