// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The captured-organization auto-enrich sweep over the REAL River runner
// (ADR-0072/A118): compose.NewJobRunner registers the sweep RunOnStart exactly
// as cmd/worker wires it, so Start fires one pass. Against a domain-named
// captured org with the flag on, that pass must create a system-requested
// dossier, enqueue its deep read, arm the cursor, and reserve a daily-cap slot —
// the whole trigger path end to end, observed on River's own completion channel
// (no sleep, no poll). The brain is nil on purpose: the deep read the sweep
// enqueues then fails honestly, but the sweep's own job still completes, and
// what this proves is the TRIGGER, not the read (the auto-apply lane has its own
// test).

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestCaptureAutoEnrichSweepTriggersADeepReadForACapturedOrg(t *testing.T) {
	e := Setup(t)
	orgID := ids.NewV7()
	// A captured, domain-named org (name_source='domain') with a live primary
	// domain — the shape the sweep enriches. The capture_auto_enrich flag is ON
	// by default (migration 0121), so no toggle is needed.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, 'Gitex', 'domain', 'connector:gmail', 'connector:gmail')`,
			orgID, e.WS, e.Rep1); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, 'gitex.com', true, 'connector:gmail', 'connector:gmail')`, e.WS, orgID)
		return err
	}); err != nil {
		t.Fatalf("seeding the captured org: %v", err)
	}

	applyRiverSchema(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := compose.NewJobRunner(e.Pool, quiet, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
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

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	awaitKindCompleted(waitCtx, t, sub, compose.CaptureAutoEnrichSweepArgs{}.Kind())

	// The sweep created a system-requested dossier for the org...
	var readCount int
	var requestedBy string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*), coalesce(max(requested_by), '') FROM site_read WHERE organization_id = $1`,
			orgID).Scan(&readCount, &requestedBy)
	}); err != nil {
		t.Fatalf("reading the dossier: %v", err)
	}
	if readCount != 1 || requestedBy != "system:capture_auto_enrich" {
		t.Fatalf("dossier count=%d requested_by=%q, want 1 / system:capture_auto_enrich", readCount, requestedBy)
	}

	// ...and armed the cursor (attempt counted, outcome queued)...
	var attempts int
	var outcome string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT attempts, last_outcome FROM capture_auto_enrich_state WHERE organization_id = $1`,
			orgID).Scan(&attempts, &outcome)
	}); err != nil {
		t.Fatalf("reading the cursor: %v", err)
	}
	if attempts != 1 || outcome != "queued" {
		t.Fatalf("cursor attempts=%d outcome=%q, want 1 / queued", attempts, outcome)
	}

	// ...and reserved exactly one daily-cap slot for this workspace's UTC day.
	var enqueued int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`).Scan(&enqueued)
	}); err != nil {
		t.Fatalf("reading the budget: %v", err)
	}
	if enqueued != 1 {
		t.Fatalf("reserved %d cap slots, want exactly 1", enqueued)
	}
}
