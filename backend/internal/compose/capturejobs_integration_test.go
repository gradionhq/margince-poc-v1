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
)

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
