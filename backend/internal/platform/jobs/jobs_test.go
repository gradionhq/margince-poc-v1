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
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/migrations"
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

// migratedAppPool resets the schema as owner, applies core+custom and the
// River schema, and returns a pool on the runtime app role — proving the
// grants in jobs.Migrate actually let the app role reach River's tables.
func migratedAppPool(t *testing.T) *jobs.Runner {
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
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating core+custom: %v", err)
	}

	// River schema is applied on the owner pool, exactly as cmd/migrate does.
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	if _, err := jobs.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("applying river schema: %v", err)
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
	return r
}

func TestRunnerStartsAndStopsCleanlyAsAppRole(t *testing.T) {
	r := migratedAppPool(t)
	ctx := t.Context()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestInserterInsertsAJobRow proves the insert-only client (the api role's
// shape: no queues, no workers, never Started) can still durably enqueue a
// row the worker's client would pick up by Kind. migratedAppPool applies the
// River schema and grants first; a second app-role pool is opened here to
// drive the inserter and to read back the row without widening Runner's API
// with a Client() accessor.
func TestInserterInsertsAJobRow(t *testing.T) {
	migratedAppPool(t) // migrates schema + grants on the test DB as a side effect
	ctx := t.Context()

	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	appPool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	ins, err := jobs.NewInserter(appPool, quietLogger())
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	if err := ins.Insert(ctx, noopArgs{}, nil); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var n int
	if err := appPool.QueryRow(ctx,
		`SELECT count(*) FROM river_job WHERE kind = 'noop'`).Scan(&n); err != nil {
		t.Fatalf("counting river_job: %v", err)
	}
	if n != 1 {
		t.Fatalf("river_job noop count = %d, want 1", n)
	}
}
