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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// CaptureBackfillArgs pages ONE bounded backfill run (ADR-0063). Unique by
// args while incomplete: start and any retry converge on one job.
type CaptureBackfillArgs struct {
	Workspace  string `json:"workspace"`
	BackfillID string `json:"backfill_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureBackfillArgs) Kind() string { return "capture_backfill" }

// captureBackfillWorker pages a run to completion, yielding between pages
// (snooze) so a long mailbox never monopolizes a worker slot. A page error
// returns nil after the engine recorded it — the run row owns its state.
type captureBackfillWorker struct {
	river.WorkerDefaults[CaptureBackfillArgs]
	registry *capture.Registry
	log      *slog.Logger
}

// backfillPagesPerTick bounds how many pages one Work invocation walks before
// yielding. ONE page: a page is up to 100 messages fetched serially, each a
// full RAW download of a real email (megabytes for a photo), so a page can
// take minutes on a live mailbox. Committing after every page and snoozing
// means each page runs under a FRESH job context — a slow page can never
// starve the next of its deadline — and the meter climbs per page.
const backfillPagesPerTick = 1

// backfillTimeout overrides River's 1-minute default: one page of large real
// messages fetched serially over the network needs real headroom, or the job
// context dies mid-page and both the fetch and the failure-recording write
// fail as a spurious "unreachable". (Matches the voice-build precedent of
// overriding the default for a multi-call, network-bound job.)
const backfillTimeout = 8 * time.Minute

// Timeout gives one page-per-tick room to finish over a live mailbox.
func (w *captureBackfillWorker) Timeout(*river.Job[CaptureBackfillArgs]) time.Duration {
	return backfillTimeout
}

func (w *captureBackfillWorker) Work(ctx context.Context, job *river.Job[CaptureBackfillArgs]) error {
	ws, err := ids.Parse(job.Args.Workspace)
	if err != nil {
		return fmt.Errorf("capture_backfill: workspace id: %w", err)
	}
	bfID, err := ids.Parse(job.Args.BackfillID)
	if err != nil {
		return fmt.Errorf("capture_backfill: backfill id: %w", err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	for i := 0; i < backfillPagesPerTick; i++ {
		done, completed, err := w.registry.RunBackfillStep(wsCtx, bfID)
		if err != nil {
			// The engine recorded the failure class on the run; the log
			// carries the detail. The row owns retry policy, not River.
			w.log.WarnContext(ctx, "capture backfill page failed", "backfill", job.Args.BackfillID, "err", err)
			return nil
		}
		if completed {
			// The connect-time import just closed: build today's digest now so
			// the morning screen reflects the freshly-imported history instead
			// of waiting for the nightly pass. Best-effort — a failed enqueue
			// never fails the backfill (the nightly run still covers it), and
			// the digest job is unique-by-state so a duplicate offer is inert.
			w.enqueueDigest(ctx, job.Args.BackfillID)
		}
		if done {
			return nil
		}
	}
	return river.JobSnooze(time.Second)
}

// enqueueDigest offers a same-day digest build through the ambient River
// client; the digest worker rebuilds the day idempotently (as-of-now truths).
// The Safely variant is deliberate: a unit test may drive Work directly with
// no River client in context, and the plain ClientFromContext PANICS there —
// a best-effort enqueue must degrade to a no-op, never crash the pager.
//
// No active-state uniqueness here (unlike the periodic sweep): a digest that
// is already RUNNING may have snapshotted the workspace BEFORE this backfill's
// rows landed, so deduping against it would drop the freshly-imported history
// off the morning screen until the nightly pass. A completion must guarantee a
// rebuild that sees its own data, so it always enqueues a fresh one — cheap
// and idempotent (the build replaces the day). One completion fires once, so
// this is not a fan-out.
func (w *captureBackfillWorker) enqueueDigest(ctx context.Context, backfillID string) {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return
	}
	if _, err := client.Insert(ctx, CaptureDigestArgs{}, nil); err != nil {
		w.log.WarnContext(ctx, "capture backfill: digest enqueue failed",
			"backfill", backfillID, "err", err)
	}
}
