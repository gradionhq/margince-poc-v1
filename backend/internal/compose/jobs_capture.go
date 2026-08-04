// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Capture's job surface, in two halves. How mail gets pulled on a schedule —
// the Gmail dispatch pass, the per-connection sync it fans out to, and the
// push-watch renewal that keeps Gmail notifying us at all — and what then
// happens to a captured message, which is the pipeline the second half wires.
// jobs.go composes every job and is the home of none.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// addGmailCaptureJobs registers every worker that reaches a Google mailbox,
// and registers none of them without the connector registry: each one resolves
// a connection and its credentials through it, so a role with no OAuth app
// configured could only fail whatever it picked up. The push-watch pair takes a
// SECOND condition — a Pub/Sub topic — because a watch registered against no
// topic notifies nobody; a deployment that never opted into push keeps the
// poll, which is the rest of this block.
//
// The dispatchers' schedules stay in the runner's own list, the posture the
// overlay and embed-reindex wiring take: periodicFor resolves them from the
// same declaration this gate reads.
func addGmailCaptureJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) {
	if cfg.GmailRegistry == nil {
		return
	}
	digests := &captureDigestWorker{registry: cfg.GmailRegistry, pool: pool, log: log}
	addDeclaredWorker[CaptureDigestArgs](reg, digests)
	addDeclaredWorker[CaptureDigestWorkspaceArgs](reg, &captureDigestWorkspaceWorker{digests: digests})
	addDeclaredWorker[GmailSyncArgs](reg, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
	// The sync dispatcher scans every registered connector, so a Google
	// Calendar connection (the same Google OAuth app) syncs on the identical
	// per-connection path a mailbox does — there is no gcal-specific job.
	// Per-connection pacing lives in the registry's scheduling sidecar
	// (next_sync_at = success + --gmail-sync-interval), which is why the
	// dispatcher's own cadence can be frequent without meaning frequent
	// provider calls.
	addDeclaredWorker[CaptureSyncArgs](reg, &captureSyncWorker{registry: cfg.GmailRegistry, log: log})
	// Backfill jobs are enqueued by the api (start op); the worker role only
	// needs the pager registered.
	addDeclaredWorker[CaptureBackfillArgs](reg, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log})
	if cfg.GmailWatch.Topic == "" {
		return
	}
	addDeclaredWorker[GmailWatchArgs](reg, &gmailWatchWorker{
		registry: cfg.GmailRegistry, renewWithin: cfg.GmailWatch.RenewWithin, log: log,
	})
	addDeclaredWorker[GmailWatchRenewArgs](reg, &gmailWatchRenewWorker{
		registry: cfg.GmailRegistry, topic: cfg.GmailWatch.Topic,
	})
}

// addCapturePipelineJobs registers what surrounds the pull above: the Telegram
// ingest that admits a message, the passes that read one already captured, and
// the outbound send.
//
// Every gate here is on its own block, which is what tells this half from the
// Gmail half: those workers share one dependency and so are one conditional,
// while these depend on different things and several on nothing at all. The
// two analysis passes that register without a model lane each say below why a
// missing lane is not a reason to leave their work undone.
func addCapturePipelineJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) {
	// The Telegram ingest job is not periodic — a poll enqueues one per accepted
	// update in the same transaction as the raw capture row; the worker role only
	// needs the worker registered. Registered unconditionally: unlike
	// Gmail/Graph, a channel connection carries its own per-connection
	// credential (no deployment-wide OAuth app to gate on), so there is nothing
	// to check for before wiring it up.
	addDeclaredWorker[TelegramIngestArgs](reg, newTelegramIngestWorker(pool, cfg.CaptureConfig, log))
	// The captured-organization auto-enrich sweep (ADR-0072/A118): always
	// registered, it enqueues system deep reads the site worker applies.
	autoEnrich := newCaptureAutoEnrichSweepWorker(pool, log)
	addDeclaredWorker[CaptureAutoEnrichSweepArgs](reg, autoEnrich)
	addDeclaredWorker[CaptureAutoEnrichWorkspaceArgs](reg, &captureAutoEnrichWorkspaceWorker{sweeper: autoEnrich})
	// The outbound send is not periodic — the api stages one job per accepted
	// message, in the same transaction as the activity; this role only needs
	// the worker registered.
	if cfg.SendRegistry != nil {
		addDeclaredWorker[SendEmailArgs](reg, newSendWorker(pool, cfg.SendRegistry, cfg.SendPacing))
	}

	if cfg.ClassifyBrain != nil {
		addDeclaredWorker[CaptureClassifyArgs](reg, &captureClassifyWorker{pool: pool})
		addDeclaredWorker[CaptureClassifyWorkspaceArgs](reg, &captureClassifyWorkspaceWorker{
			classifier: NewCaptureClassifier(pool, cfg.ClassifyBrain, log),
		})
	}

	if cfg.EnrichBrain != nil {
		addDeclaredWorker[CaptureEnrichArgs](reg, &captureEnrichWorker{pool: pool})
		addDeclaredWorker[CaptureEnrichWorkspaceArgs](reg, &captureEnrichWorkspaceWorker{
			enricher: NewCaptureEnricher(pool, cfg.EnrichBrain, log),
		})
	}

	// Registered unconditionally: the org-name promotion weighs evidence rows
	// the enrich pass already wrote, so it needs no model. Gating it on a brain
	// would leave an AI-less deployment unable to act on signatures it had
	// already collected.
	addDeclaredWorker[OrgNamePromotionArgs](reg, &orgNamePromotionWorker{pool: pool})
	addDeclaredWorker[OrgNamePromotionWorkspaceArgs](reg, &orgNamePromotionWorkspaceWorker{promoter: NewOrgNamePromoter(pool, log)})

	// Registered unconditionally for a different reason: only the counterparty
	// verdict's JUDGING stage needs a model, and the worker skips that stage
	// when none is configured. Gating the whole worker on a brain would mean an
	// AI-less deployment never staged a review for an existing unsure row and
	// never redacted mail it had already hidden.
	addDeclaredWorker[CounterpartyVerdictArgs](reg, &counterpartyVerdictWorker{pool: pool})
	addDeclaredWorker[CounterpartyVerdictWorkspaceArgs](reg, &counterpartyVerdictWorkspaceWorker{
		engine: NewCounterpartyVerdictEngine(pool, cfg.VerdictBrain, log),
	})
}

// GmailWatchConfig configures the Gmail push-watch maintenance pass. Topic is
// the Pub/Sub topic Gmail publishes change notifications to (empty disables the
// pass entirely — capture stays on the poll); Interval is the scan cadence; and
// RenewWithin is how far ahead of a watch's expiry it is re-registered.
type GmailWatchConfig struct {
	Topic       string
	Interval    time.Duration
	RenewWithin time.Duration
}

// GmailSyncArgs schedules one DISPATCH pass: scan the fleet for due Gmail
// connections (the sidecar's backoff/pacing gate, ADR-0063) and enqueue one
// CaptureSyncArgs job per connection. The dispatcher never syncs inline —
// per-connection jobs isolate failures and kill head-of-line blocking.
type GmailSyncArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GmailSyncArgs) Kind() string { return "gmail_sync" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (GmailSyncArgs) FleetWide() {}

// gmailSyncWorker is the dispatcher: due-scan, then one insert per
// connection. Uniqueness on the connection id means a still-running or
// already-queued sync is not double-enqueued; only a fleet-enumeration
// failure is returned (so River retries the tick).
type gmailSyncWorker struct {
	registry *capture.Registry
	log      *slog.Logger
}

func (w *gmailSyncWorker) Work(ctx context.Context, _ *river.Job[GmailSyncArgs]) error {
	var enumErr error
	for _, desc := range w.registry.Connectors() {
		due, err := w.registry.DueConnections(ctx, desc.Name)
		if err != nil {
			enumErr = errors.Join(enumErr, err)
		}
		for _, d := range due {
			if err := dispatchOne(ctx, CaptureSyncArgs{
				Workspace:    d.Workspace.UUID,
				ConnectionID: d.ID.String(),
				Provider:     desc.Name,
			}, nil); err != nil {
				// A refused enqueue means this connection is never synced, so
				// it fails the DISPATCHER — the same posture as the watch
				// dispatcher below, which this one is the mirror of.
				enumErr = errors.Join(enumErr, fmt.Errorf("enqueueing the sync for connection %s: %w", d.ID, err))
			}
		}
	}
	return jobs.FaultContext(ctx, enumErr)
}

// CaptureSyncArgs syncs ONE connection. Unique by args while incomplete, so
// the dispatcher and the (future) push webhook can both enqueue without
// double-running a mailbox.
type CaptureSyncArgs struct {
	Workspace    ids.UUID `json:"workspace_id"`
	ConnectionID string   `json:"connection_id"`
	Provider     string   `json:"provider"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureSyncArgs) Kind() string { return "capture_sync" }

// WorkspaceID binds this connection's sync to its tenant (jobs.WorkspaceScoped).
func (a CaptureSyncArgs) WorkspaceID() ids.UUID { return a.Workspace }

// captureSyncWorker runs one SyncOnce under the connection's workspace. A
// sync failure returns nil after the registry has recorded it: the sidecar's
// backoff owns the retry cadence (ADR-0063) — a River retry would bypass it.
type captureSyncWorker struct {
	registry *capture.Registry
	log      *slog.Logger
}

func (w *captureSyncWorker) Work(ctx context.Context, job *river.Job[CaptureSyncArgs]) error {
	conn, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("capture_sync: connection id: %w", err))
	}
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if err := w.registry.SyncOnce(wsCtx, conn); err != nil {
		w.log.WarnContext(ctx, "capture connection sync failed",
			"connection", job.Args.ConnectionID, "provider", job.Args.Provider, "err", err)
	}
	return nil
}

// GmailWatchArgs schedules one push-watch maintenance pass: register a Gmail
// users.watch for every active connection that has none yet and renew any
// nearing its 7-day expiry (capture.md CAP-DDL-2). Scheduled only when a
// Pub/Sub topic is configured; without one, no watch job runs and capture stays
// on the poll (GmailSyncArgs).
type GmailWatchArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GmailWatchArgs) Kind() string { return "gmail_watch_renew" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (GmailWatchArgs) FleetWide() {}

// gmailWatchWorker walks the fleet's active Gmail connections whose watch is
// missing or nearing expiry and enqueues ONE renewal job per connection,
// against the configured Pub/Sub topic. It mirrors gmailSyncWorker — the same
// collectDue-shaped walk, keyed on the renewal deadline instead of the sync
// cursor — and it fans out at the same granularity, because a watch belongs to
// a connection rather than to a workspace: a mailbox whose renewal fails is one
// connection's problem, and per-connection jobs keep it from being read as the
// tenant's.
type gmailWatchWorker struct {
	registry    *capture.Registry
	renewWithin time.Duration
	log         *slog.Logger
}

func (w *gmailWatchWorker) Work(ctx context.Context, _ *river.Job[GmailWatchArgs]) error {
	due, enumErr := w.registry.DueWatches(ctx, "gmail", w.renewWithin)
	for _, d := range due {
		if err := dispatchOne(ctx, GmailWatchRenewArgs{
			Workspace:    d.Workspace.UUID,
			ConnectionID: d.ID.String(),
		}, nil); err != nil {
			// A refused enqueue means this connection's watch is never renewed,
			// so it fails the DISPATCHER rather than being logged past.
			enumErr = errors.Join(enumErr, fmt.Errorf("enqueueing the watch renewal for connection %s: %w", d.ID, err))
		}
	}
	return jobs.FaultContext(ctx, enumErr)
}

// GmailWatchRenewArgs renews ONE connection's push watch. The workspace travels
// with it because capture_connection is RLS-scoped and a job carries no
// session.
type GmailWatchRenewArgs struct {
	Workspace    ids.UUID `json:"workspace_id"`
	ConnectionID string   `json:"connection_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (GmailWatchRenewArgs) Kind() string { return "gmail_watch_renew_connection" }

// WorkspaceID binds this renewal to its tenant (jobs.WorkspaceScoped).
func (a GmailWatchRenewArgs) WorkspaceID() ids.UUID { return a.Workspace }

// gmailWatchRenewWorker renews one connection's watch and advances
// watch_expires_at. A revoked mailbox fails its OWN row now, where before it
// was logged and skipped inside a pass River recorded as completed.
type gmailWatchRenewWorker struct {
	registry *capture.Registry
	topic    string
}

func (w *gmailWatchRenewWorker) Work(ctx context.Context, job *river.Job[GmailWatchRenewArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	connID, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("gmail_watch_renew_connection: connection id: %w", err))
	}
	return jobs.FaultContext(ctx, w.registry.RenewWatch(wsCtx, connID, w.topic))
}
