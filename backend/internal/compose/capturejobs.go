// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture pipeline's overnight River trio (ADR-0063): the catch-up
// classify pass (§2.8), the signature-enrich pass (§2.9), and the
// morning-digest build (CAP-DDL-6). Job args and worker adapters only —
// the engines they delegate to (CaptureClassifier, CaptureEnricher, the
// capture registry's digest builder) stay River-agnostic; NewJobRunner
// (jobs.go) registers these on the shared periodic schedule.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CaptureClassifyArgs runs one catch-up classify pass (ADR-0063; §2.8).
type CaptureClassifyArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CaptureClassifyArgs) Kind() string { return "capture_classify" }

// captureClassifyWorker drives the batched label engine; the engine
// commits per model call, so a mid-pass crash or budget stop loses
// nothing and the next tick resumes from the shrunken backlog.
type captureClassifyWorker struct {
	river.WorkerDefaults[CaptureClassifyArgs]
	classifier *CaptureClassifier
}

func (w *captureClassifyWorker) Work(ctx context.Context, _ *river.Job[CaptureClassifyArgs]) error {
	return w.classifier.Run(ctx, 0)
}

// CaptureEnrichArgs runs one signature-enrich pass (ADR-0063; §2.9).
type CaptureEnrichArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CaptureEnrichArgs) Kind() string { return "capture_enrich" }

// captureEnrichWorker drives the evidence-gated signature pass; every
// accepted field is auditable back to its verbatim signature line.
type captureEnrichWorker struct {
	river.WorkerDefaults[CaptureEnrichArgs]
	enricher *CaptureEnricher
}

func (w *captureEnrichWorker) Work(ctx context.Context, _ *river.Job[CaptureEnrichArgs]) error {
	return w.enricher.Run(ctx)
}

// CaptureDigestArgs builds the morning digests (CAP-DDL-6; the nightly
// suite's last pass).
type CaptureDigestArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CaptureDigestArgs) Kind() string { return "capture_digest" }

// captureDigestWorker assembles one digest per connected user per
// workspace; a re-run replaces the day's payload (as-of-now truths).
type captureDigestWorker struct {
	river.WorkerDefaults[CaptureDigestArgs]
	registry *capture.Registry
	pool     *pgxpool.Pool
	log      *slog.Logger
	// now is the injected clock (nil = wall clock). The digest day is
	// deliberately read at execution time, not enqueue time: the payload
	// is as-of-now truths and a re-run replaces the day, so a retry that
	// crosses midnight builds the morning actually being served.
	now func() time.Time
}

func (w *captureDigestWorker) Work(ctx context.Context, _ *river.Job[CaptureDigestArgs]) error {
	workspaces, err := liveWorkspaceIDs(ctx, w.pool)
	if err != nil {
		return err
	}
	clock := w.now
	if clock == nil {
		clock = time.Now
	}
	today := clock().UTC()
	// One workspace's failure must not starve the rest — but a failed
	// workspace must fail the job so River retries it rather than leaving
	// it digest-less for the day.
	var failures []error
	for _, ws := range workspaces {
		if err := w.registry.BuildDigests(principal.WithWorkspaceID(ctx, ws), today); err != nil {
			w.log.ErrorContext(ctx, "capture digest: build failed", "workspace", ws.String(), "err", err)
			failures = append(failures, fmt.Errorf("workspace %s: %w", ws.String(), err))
		}
	}
	return errors.Join(failures...)
}
