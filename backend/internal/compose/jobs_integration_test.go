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
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// TestNewJobRunnerWiresTheOverlayPollerWhenAVaultIsConfigured proves
// NewJobRunner's overlayVault-present branch actually registers the
// overlay reconcile worker/periodic job rather than silently staying off
// — the counterpart to TestRiverCloseDateSweepStagesSameProvisionalAsDirectSweep's
// overlayVault=nil call below, which never exercises this branch.
func TestNewJobRunnerWiresTheOverlayPollerWhenAVaultIsConfigured(t *testing.T) {
	e := integration.Setup(t)
	applyRiverSchema(t)

	runner, err := NewJobRunner(e.Pool, slog.New(slog.DiscardHandler), JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		OverlayVault:      keyvault.NewMemory(),
		OverlayInterval:   time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	if runner == nil {
		t.Fatal("NewJobRunner: want a non-nil Runner when an overlay vault is configured")
	}

	// NewJobRunner returns a non-nil Runner regardless of the overlayVault
	// branch, so non-nil alone proves nothing. Prove the branch actually
	// registered the reconcile worker AND its RunOnStart periodic job:
	// boot the runner and observe an overlay_reconcile completion on the
	// subscription channel. With no overlay-mode workspace seeded the sweep
	// finds nothing due and completes cleanly; if the overlayVault branch
	// were deleted, the job is never scheduled and this await times out.
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

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, OverlayReconcileArgs{}.Kind())
}

// applyRiverSchema layers River's schema onto the harness-migrated database,
// exactly as cmd/migrate does after core+custom.
//
// The presence of river_job is the condition, not a once-per-process flag.
// testdb.EnsureSchema opens the FIRST integration test in a process with
// DROP SCHEMA public CASCADE, which takes River's tables with it. A
// sync.Once could therefore record "applied" for a schema that a later
// EnsureSchema destroyed, and every subsequent call would skip as a no-op —
// leaving an enqueue to fail on a missing relation in a suite whose own
// setup looked complete. Probing the schema cannot go stale that way,
// whatever order the suites in this binary happen to run in.
//
// jobs.Migrate is not idempotent (a second call fails "river_migration
// already exists"), so the probe guards it rather than wrapping it: when
// river_job is present the migration is already applied, and when the
// schema was dropped River's own version ledger went with it, so the
// migration replays cleanly. More than one suite here needs the River
// schema, so every one goes through this — it stays the one spelling.
func applyRiverSchema(t *testing.T) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	var present bool
	if err := ownerPool.QueryRow(ctx,
		`SELECT to_regclass('public.river_job') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("probing for the river schema: %v", err)
	}
	if present {
		return
	}
	if _, err := jobs.Migrate(ctx, ownerPool); err != nil {
		t.Fatalf("applying river schema: %v", err)
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
