// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The overnight capture trio rides River (ADR-0063): with brains and a
// registry configured, NewJobRunner registers the classify, enrich and
// digest jobs and their RunOnStart passes complete — proving the
// registration branches and the worker adapters end to end, not just the
// engines they delegate to.

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A completed connect-time backfill must build the day's digest itself, so the
// morning screen reflects the freshly-imported history without waiting for the
// nightly pass. The RunOnStart digest is drained first, so the digest observed
// after the backfill completes is provably the one the completion enqueued
// (not the boot pass) — and it can't have been deduped, the first already ran.
func TestBackfillCompletionBuildsTheDigest(t *testing.T) {
	b := setupBackfillWire(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The run's job is enqueued by hand further down, once the boot digest has
	// been drained — so the start itself deliberately schedules nothing.
	run, err := b.registry.StartBackfill(b.human, "gmail", ids.From[ids.UserKind](b.env.Rep1), 6, 25,
		func(context.Context, pgx.Tx, ids.UUID) error { return nil })
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}

	runner, err := NewJobRunner(b.env.Pool, quiet, JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		GmailRegistry:     b.registry,
		ClassifyBrain:     &scriptedClassifyBrain{},
		EnrichBrain:       &signatureScriptBrain{},
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()

	ctx := context.Background()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Drain the boot digest so the next one cannot be it — nor deduped by it.
	awaitKindCompleted(waitCtx, t, sub, CaptureDigestWorkspaceArgs{}.Kind())

	// The boot pass placed its own dispatcher row, so "no dispatcher exists" is
	// not the claim to make — "the completion added none" is. Read the count
	// before the backfill runs and hold it to that afterwards.
	dispatchersBefore := countJobsOfKind(ctx, t, b, CaptureDigestArgs{}.Kind())

	// Now schedule the backfill; the worker pages it to done and enqueues the
	// same-day digest off the completion edge.
	if err := runner.Enqueue(ctx, CaptureBackfillArgs{
		Workspace: b.env.WS, BackfillID: run.ID.String(),
	}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	awaitKindCompleted(waitCtx, t, sub, "capture_backfill")
	// The digest that follows the completed backfill is the payoff wiring.
	awaitKindCompleted(waitCtx, t, sub, CaptureDigestWorkspaceArgs{}.Kind())

	// The waits above are satisfied by a digest of the right KIND, which the
	// boot pass also produces — they prove the wiring fires, not WHAT it
	// fires. What follows is the fix itself, and each half fails silently on a
	// single-workspace fixture, where a fleet fan-out and a one-off enqueue
	// produce the same visible outcome.

	// A dispatcher row added here would be one workspace's backfill running
	// the digest for every workspace in the installation.
	if after := countJobsOfKind(ctx, t, b, CaptureDigestArgs{}.Kind()); after != dispatchersBefore {
		t.Errorf("capture_digest rows went %d -> %d across the backfill; a completion must enqueue the "+
			"CHILD kind for its own workspace, never the dispatcher that fans out over the whole fleet",
			dispatchersBefore, after)
	}

	// An UNTAGGED child for this workspace is what a one-off enqueue looks
	// like, and it is the row the boot fan-out cannot have written: every
	// child of a fleet pass is tagged at the dispatch chokepoint. So finding
	// one is proof the completion enqueued it, and proof it will not be
	// counted as a fleet pass by the sweep gauges.
	rows, err := b.env.Pool.Query(ctx,
		`SELECT coalesce(args->>'workspace_id', ''), tags FROM river_job WHERE kind = $1 ORDER BY id`,
		CaptureDigestWorkspaceArgs{}.Kind())
	if err != nil {
		t.Fatalf("reading digest rows: %v", err)
	}
	defer rows.Close()

	var oneOffs int
	for rows.Next() {
		var workspace string
		var tags []string
		if err := rows.Scan(&workspace, &tags); err != nil {
			t.Fatalf("scanning a digest row: %v", err)
		}
		if slices.Contains(tags, jobs.SweepTag) {
			continue // the boot fan-out's own child
		}
		oneOffs++
		if workspace != b.env.WS.String() {
			t.Errorf("the one-off digest names workspace %q, want the finishing tenant's %q", workspace, b.env.WS)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading digest rows: %v", err)
	}
	if oneOffs != 1 {
		t.Errorf("%d untagged capture_digest_workspace row(s), want exactly 1 — the completion's own digest, "+
			"tagged as a fleet pass or never enqueued at all", oneOffs)
	}
}

// countJobsOfKind answers how many river_job rows of a kind exist right now.
func countJobsOfKind(ctx context.Context, t *testing.T, b *backfillWireEnv, kind string) int {
	t.Helper()
	var n int
	if err := b.env.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatalf("counting %s rows: %v", kind, err)
	}
	return n
}

func TestCaptureOvernightJobsRegisterAndRun(t *testing.T) {
	b := setupBackfillWire(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	runner, err := NewJobRunner(b.env.Pool, quiet, JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		GmailRegistry:     b.registry,
		// Zero-value scripts: the classify brain labels whatever backlog
		// the fake connector synced; the enrich pass finds no
		// connector-created person (this registry wires no ensurer) and
		// completes as an honest no-op.
		ClassifyBrain: &scriptedClassifyBrain{},
		EnrichBrain:   &signatureScriptBrain{},
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()

	ctx := context.Background()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, kind := range []string{"capture_classify", "capture_enrich", "capture_digest"} {
		awaitKindCompleted(waitCtx, t, sub, kind)
	}
}
