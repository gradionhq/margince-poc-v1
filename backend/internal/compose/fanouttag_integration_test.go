// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The sweep tag survives a REAL insert, through both chokepoints. Every unit
// test beside this one reads the opts the helpers BUILT, and the sweep gauges
// read a river_job column — so a River release that stopped persisting tags
// from InsertOpts, or an opts merge that dropped them, would empty both gauges
// in production with nothing going red. Both fan-out shapes are covered
// because they reach River by different methods (InsertMany for the fleet,
// Insert for one connection) and a regression can take one without the other.

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fanOutProbeArgs is a fixture DISPATCHER. It exists because both chokepoints
// resolve the River client from the context of the job being worked, so the
// only honest way to drive them is from inside a real one. Like
// timeout_probe (jobtimeout_integration_test.go) it cannot live in
// api/jobs.yaml — the generated closed union names a Go type per declared
// kind, and no production type answers for a fixture.
type fanOutProbeArgs struct{}

func (fanOutProbeArgs) Kind() string { return "fan_out_probe" }

// fanOutProbeWorker fans out both ways and reports what happened. It reports
// on a channel AND returns the error: the channel is how the test sees a
// refused insert as a failed assertion rather than a timeout, and the return
// is what keeps a failed dispatch out of River's completed set.
type fanOutProbeWorker struct {
	river.WorkerDefaults[fanOutProbeArgs]
	fleet []ids.UUID
	child CaptureSyncArgs
	done  chan error
}

func (w *fanOutProbeWorker) Work(ctx context.Context, _ *river.Job[fanOutProbeArgs]) error {
	err := dispatchWith(ctx, w.fleet, clientInsertMany(ctx),
		workspaceSweepOpts(CloseDateWorkspaceArgs{}.Kind()), closeDateWorkspaceArgsFor)
	if err == nil {
		err = dispatchOne(ctx, w.child, nil)
	}
	w.done <- err
	return err
}

// inertWorker registers a child kind so River accepts the insert. The
// assertions read what the INSERT wrote; what the child would have done is
// beside the point.
type inertWorker[T river.JobArgs] struct {
	river.WorkerDefaults[T]
}

func (inertWorker[T]) Work(context.Context, *river.Job[T]) error { return nil }

func TestARealFanOutInsertCarriesTheSweepTagIntoRiversTagsColumn(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)

	fleetChild := ids.NewV7()
	loopChild := CaptureSyncArgs{
		Workspace:    ids.NewV7(),
		ConnectionID: ids.NewV7().String(),
		Provider:     "gmail",
	}
	probe := &fanOutProbeWorker{
		fleet: []ids.UUID{fleetChild},
		child: loopChild,
		done:  make(chan error, 1),
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, probe)
	river.AddWorker(workers, &inertWorker[CloseDateWorkspaceArgs]{})
	river.AddWorker(workers, &inertWorker[CaptureSyncArgs]{})

	runner, err := jobs.New(e.Pool, jobs.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	ctx := t.Context()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	if err := runner.Enqueue(ctx, fanOutProbeArgs{}, nil); err != nil {
		t.Fatalf("enqueueing the probe dispatcher: %v", err)
	}

	select {
	case err := <-probe.done:
		if err != nil {
			t.Fatalf("the probe dispatcher's fan-out failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the probe dispatcher never ran; nothing was fanned out")
	}

	assertRowIsTagged(ctx, t, e.Pool, CloseDateWorkspaceArgs{}.Kind(), fleetChild,
		"dispatchWith's fleet insert")
	assertRowIsTagged(ctx, t, e.Pool, loopChild.Kind(), loopChild.Workspace,
		"dispatchOne's single insert")
}

// assertRowIsTagged reads the tags River actually stored for one child. It
// matches on the workspace inside args so the row is the one this test
// enqueued, not a leftover of the same kind.
func assertRowIsTagged(ctx context.Context, t *testing.T, pool *pgxpool.Pool, kind string, workspace ids.UUID, via string) {
	t.Helper()

	var tags []string
	if err := pool.QueryRow(ctx,
		`SELECT tags FROM river_job WHERE kind = $1 AND args->>'workspace_id' = $2`,
		kind, workspace.String()).Scan(&tags); err != nil {
		t.Fatalf("reading back the %s child enqueued through %s: %v", kind, via, err)
	}
	if !slices.Contains(tags, jobs.SweepTag) {
		t.Errorf("%s stored tags %v for the %s child, without %q — the tag did not survive "+
			"the insert path, and both sweep gauges read that column",
			via, tags, kind, jobs.SweepTag)
	}
}
