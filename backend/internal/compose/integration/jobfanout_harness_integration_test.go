// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ceremony every fan-out suite shares: an extra tenant to fan out to, a
// job runner subscribed before it starts, and the two awaits that read River's
// event stream instead of polling a table. Each converted pass asserts the same
// three things about itself — one row per tenant, each naming its tenant on the
// wire, and only the failed tenant's row failing — so the reading of River's
// events belongs here rather than once per suite.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedExtraWorkspace mints an additional tenant. archived names a workspace
// nobody looks at any more, which still holds everything it held the day it was
// archived — some passes are owed on it and some deliberately skip it, so a
// fan-out suite states which by seeding one.
func seedExtraWorkspace(t *testing.T, owner *pgx.Conn, name string, archived bool) ids.UUID {
	t.Helper()
	ws := ids.NewV7()
	archivedAt := "NULL"
	if archived {
		archivedAt = "now()"
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO workspace (id, name, slug, base_currency, archived_at)
		VALUES ($1, $2, $3, 'EUR', `+archivedAt+`)`, ws, name, name+"-"+ws.String()); err != nil {
		t.Fatalf("seeding the %s workspace: %v", name, err)
	}
	return ws
}

// recordWorkspaceJobOutcome files one workspace job's outcome under the tenant
// its args name, reading the WIRE key rather than a decoded args struct — the
// same `workspace_id` every per-workspace read of river_job selects.
func recordWorkspaceJobOutcome(t *testing.T, into map[string]bool, ev *river.Event, kind string, completed bool) {
	t.Helper()
	if ev == nil || ev.Job == nil || ev.Job.Kind != kind {
		return
	}
	var args struct {
		Workspace string `json:"workspace_id"`
	}
	if err := json.Unmarshal(ev.Job.EncodedArgs, &args); err != nil {
		t.Fatalf("decoding the %s args River persisted: %v", kind, err)
	}
	if args.Workspace == "" {
		t.Fatalf("a %s row carries no workspace_id — the tenant it worked for is invisible to every per-workspace read of river_job", kind)
	}
	into[args.Workspace] = completed
}

// awaitWorkspaceJobOutcomes collects one outcome per tenant until want distinct
// workspaces have reported, or the deadline fires. No polling, no sleep.
func awaitWorkspaceJobOutcomes(ctx context.Context, t *testing.T, completed, failed <-chan *river.Event, kind string, want int) map[string]bool {
	t.Helper()
	outcomes := make(map[string]bool, want)
	for len(outcomes) < want {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out with %d of %d %s outcomes: %v", len(outcomes), want, kind, ctx.Err())
		case ev := <-completed:
			recordWorkspaceJobOutcome(t, outcomes, ev, kind, true)
		case ev := <-failed:
			recordWorkspaceJobOutcome(t, outcomes, ev, kind, false)
		}
	}
	return outcomes
}

// awaitKindsCompleted blocks until every named kind has reported one
// completion, or the deadline fires. No polling, no sleep.
func awaitKindsCompleted(ctx context.Context, t *testing.T, completed <-chan *river.Event, kinds ...string) {
	t.Helper()
	pending := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		pending[kind] = struct{}{}
	}
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting on %d of %v to complete: %v", len(pending), kinds, ctx.Err())
		case ev := <-completed:
			if ev != nil && ev.Job != nil {
				delete(pending, ev.Job.Kind)
			}
		}
	}
}

// dispatchInterval is the cadence a repeat-schedule suite configures, and
// dispatchGapBound is what separates "scheduled on the configured interval"
// from "scheduled on some larger constant": three times the interval, which
// leaves a correct schedule ample slack while excluding every constant actually
// in reach — dispatchScanInterval, and the tens-of-seconds defaults these flags
// carry, which are the likeliest miswiring of all. The bound is on the GAP
// between two dispatches rather than on the whole run, because a deadline on the
// run would also pass for any constant smaller than the deadline.
const (
	dispatchInterval = 2 * time.Second
	dispatchGapBound = 3 * dispatchInterval
)

// awaitTwoDispatchArrivals blocks until two DISTINCT jobs of kind have
// completed, and reports when each arrived. It is the observer's clock, not the
// job's own timestamps: what a repeat-schedule suite is asking is whether a
// SECOND dispatch happened soon, and the reader can trust the arrival.
//
// RunOnStart fires once whatever the cadence is, so the first arrival proves
// nothing about the schedule and every such suite needs the second — which is
// why this waits here rather than once per pass.
func awaitTwoDispatchArrivals(ctx context.Context, t *testing.T, completed <-chan *river.Event, kind string) (first, second time.Time) {
	t.Helper()
	seen := make(map[int64]struct{}, 2)
	for len(seen) < 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("saw %d of 2 %s dispatches: %v — the pass fired at boot and then never again, so the operator's interval is not what schedules it",
				len(seen), kind, ctx.Err())
		case ev := <-completed:
			if ev == nil || ev.Job == nil || ev.Job.Kind != kind {
				continue
			}
			if _, duplicate := seen[ev.Job.ID]; duplicate {
				continue
			}
			seen[ev.Job.ID] = struct{}{}
			if len(seen) == 1 {
				first = time.Now()
			} else {
				second = time.Now()
			}
		}
	}
	return first, second
}

// startTestJobRunner boots a worker-role job runner over cfg and returns it
// with its completion and failure channels, subscribed BEFORE Start so the
// RunOnStart round's outcomes are never missed. The runner is stopped in
// cleanup.
func startTestJobRunner(t *testing.T, pool *pgxpool.Pool, cfg compose.JobRunnerConfig) (*jobs.Runner, <-chan *river.Event, <-chan *river.Event) {
	t.Helper()
	runner, err := compose.NewJobRunner(pool, slog.New(slog.DiscardHandler), cfg)
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	completed, cancelCompleted := runner.SubscribeCompleted()
	t.Cleanup(cancelCompleted)
	failed, cancelFailed := runner.SubscribeFailed()
	t.Cleanup(cancelFailed)
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return runner, completed, failed
}
