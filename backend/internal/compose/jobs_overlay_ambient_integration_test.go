// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The sweep enqueues its re-fetches through the River client that is working
// the sweep's own job, so the poller carries no second client. This is that
// resolution proved against a real client and a real river_job table: a job
// context inserts, and a context with no client is reported rather than
// swallowed — the difference between a class the next pass re-enqueues and a
// class that silently never converges while the flip stays blocked.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

func TestAmbientRefetchEnqueuerInsertsThroughTheClientWorkingTheJob(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	ctx := context.Background()
	// The client is built here rather than taken from platform/jobs because
	// rivertest.WorkContext needs the *river.Client itself, which the Runner
	// holds unexported — an insert-only client, exactly the shape jobs.NewInserter
	// builds, so what this drives is the real insert path and not a stand-in.
	client, err := river.NewClient(riverpgxv5.New(e.Pool), &river.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("river.NewClient: %v", err)
	}

	args := OverlayRefetchArgs{
		Workspace:      e.WS,
		IncumbentClass: overlay.IncumbentClassCalls,
		ExternalID:     "calls:123",
	}
	if err := (ambientRefetchEnqueuer{}).Enqueue(rivertest.WorkContext(ctx, client), args, reprojectionInsertOpts()); err != nil {
		t.Fatalf("Enqueue on a job's own context: %v", err)
	}

	job := rivertest.RequireInserted(ctx, t, riverpgxv5.New(e.Pool), OverlayRefetchArgs{}, nil)
	if job.Args != args {
		t.Fatalf("the inserted job carries %+v, want %+v — a re-fetch that names another record converges nothing", job.Args, args)
	}
}

func TestAmbientRefetchEnqueuerReportsAContextCarryingNoClient(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	args := OverlayRefetchArgs{Workspace: e.WS, IncumbentClass: overlay.IncumbentClassCalls, ExternalID: "calls:123"}

	err := ambientRefetchEnqueuer{}.Enqueue(context.Background(), args, reprojectionInsertOpts())
	if err == nil {
		t.Fatal("Enqueue off a job context returned nil — a re-fetch that was never inserted must not read as one that was")
	}
	if n := countJobsOfKind(context.Background(), t, e.Pool, args.Kind()); n != 0 {
		t.Fatalf("%d %s row(s) after a refused enqueue, want 0", n, args.Kind())
	}
}
