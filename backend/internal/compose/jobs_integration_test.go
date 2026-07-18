// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The behaviour-preserving proof for the River swap: the
// close-date sweep reached through a River periodic job stages the IDENTICAL
// provisional correction the direct Sweep test asserts
// (TestCloseDateSweepStagesProvisionalForForecastBearingDeal). The domain
// seam (deals.Sweep) is unchanged; this proves the scheduler swap does not
// change the outcome. Completion is observed on River's subscription
// channel, bounded by a deadline — never a sleep.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// riverSchemaOnce guards the River migration per test-binary process, the
// same contract as testdb.EnsureSchema: the schema survives the per-test
// Truncate, but the truncate DOES empty river_migration's applied-version
// ledger — so a second in-process migrate would re-run River's first
// migration against tables that still exist and fail on CREATE TABLE.
// Every suite that needs River must go through applyRiverSchema so this
// stays the one spelling.
var (
	riverSchemaOnce sync.Once
	riverSchemaErr  error
)

// applyRiverSchema layers River's schema onto the harness-migrated database,
// exactly as cmd/migrate does after core+custom — once per process.
func applyRiverSchema(t *testing.T) {
	t.Helper()
	riverSchemaOnce.Do(func() {
		ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
		if ownerDSN == "" {
			riverSchemaErr = errors.New("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
			return
		}
		ctx := context.Background()
		ownerPool, err := database.NewPool(ctx, ownerDSN)
		if err != nil {
			riverSchemaErr = fmt.Errorf("opening owner pool: %w", err)
			return
		}
		defer ownerPool.Close()
		if _, err := jobs.Migrate(ctx, ownerPool); err != nil {
			riverSchemaErr = fmt.Errorf("applying river schema: %w", err)
		}
	})
	if riverSchemaErr != nil {
		t.Fatal(riverSchemaErr)
	}
}

// awaitKindCompleted blocks until a job of the given kind reports completion,
// or the context deadline fires. No polling, no sleep.
func awaitKindCompleted(ctx context.Context, t *testing.T, sub <-chan *river.Event, kind string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q to complete: %v", kind, ctx.Err())
		case ev := <-sub:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				return
			}
		}
	}
}

func TestRiverCloseDateSweepStagesSameProvisionalAsDirectSweep(t *testing.T) {
	e := setupCloseDate(t)
	applyRiverSchema(t)
	// The exact fixture the direct-Sweep test uses: an overdue, active,
	// commit-override deal — never auto-final, always a staged proposal.
	id := e.seedSweepDeal(t, "Commit slipped", e.late, stringp("commit"), intp(-10), 3)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := NewJobRunner(e.Pool, quiet, JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	// Subscribe before Start so the RunOnStart completion is never missed.
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

	// RunOnStart enqueues both periodic passes at boot; wait for the
	// close-date sweep to complete, then assert the same outcome the direct
	// Sweep produces.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, CloseDateSweepArgs{}.Kind())

	swept := e.readSwept(t, id)
	if swept.expectedClose == nil || swept.expectedClose.Before(today()) {
		t.Fatalf("provisional date = %v — INV-CLOSE-PAST must hold immediately", swept.expectedClose)
	}
	if !swept.provisional {
		t.Error("🟡 replacement must be provisional until a human confirms")
	}
	if swept.forecastCat == nil || *swept.forecastCat != "commit" {
		t.Errorf("forecast_category = %v, want the untouched commit override", swept.forecastCat)
	}
	if got := e.pendingCorrections(t, id); got != 1 {
		t.Fatalf("pending close_date_correction approvals = %d, want 1 — the River-driven pass must stage exactly what the direct Sweep does", got)
	}
}
