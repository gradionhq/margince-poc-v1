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
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A completed connect-time backfill must build the day's digest itself, so the
// morning screen reflects the freshly-imported history without waiting for the
// nightly pass. The RunOnStart digest is drained first, so the digest observed
// after the backfill completes is provably the one the completion enqueued
// (not the boot pass) — and it can't have been deduped, the first already ran.
func TestBackfillCompletionBuildsTheDigest(t *testing.T) {
	b := setupBackfillWire(t)
	applyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	run, err := b.registry.StartBackfill(b.human, "gmail", ids.From[ids.UserKind](b.env.Rep1), 6, 25)
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
	awaitKindCompleted(waitCtx, t, sub, "capture_digest")

	// Now schedule the backfill; the worker pages it to done and enqueues the
	// same-day digest off the completion edge.
	if err := runner.Enqueue(ctx, CaptureBackfillArgs{
		Workspace: b.env.WS.String(), BackfillID: run.ID.String(),
	}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	awaitKindCompleted(waitCtx, t, sub, "capture_backfill")
	// The digest that follows the completed backfill is the payoff wiring.
	awaitKindCompleted(waitCtx, t, sub, "capture_digest")
}

func TestCaptureOvernightJobsRegisterAndRun(t *testing.T) {
	b := setupBackfillWire(t)
	applyRiverSchema(t)
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
