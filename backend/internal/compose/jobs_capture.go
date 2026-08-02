// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Capture's job surface: the Gmail dispatch pass, the per-connection sync it
// fans out to, and the push-watch renewal that keeps Gmail notifying us at all.
// These three are one concept — how captured mail gets pulled on a schedule.
// jobs.go composes every job and is the home of none.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

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
	river.WorkerDefaults[GmailSyncArgs]
	registry *capture.Registry
	log      *slog.Logger
}

func (w *gmailSyncWorker) Work(ctx context.Context, _ *river.Job[GmailSyncArgs]) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	var enumErr error
	for _, desc := range w.registry.Connectors() {
		due, err := w.registry.DueConnections(ctx, desc.Name)
		if err != nil {
			enumErr = errors.Join(enumErr, err)
		}
		for _, d := range due {
			if _, err := client.Insert(ctx, CaptureSyncArgs{
				Workspace:    d.Workspace.UUID,
				ConnectionID: d.ID.String(),
				Provider:     desc.Name,
			}, &river.InsertOpts{
				UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
			}); err != nil {
				w.log.WarnContext(ctx, "capture sync enqueue failed", "connection", d.ID.String(), "provider", desc.Name, "err", err)
			}
		}
	}
	return enumErr
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
	river.WorkerDefaults[CaptureSyncArgs]
	registry *capture.Registry
	log      *slog.Logger
}

func (w *captureSyncWorker) Work(ctx context.Context, job *river.Job[CaptureSyncArgs]) error {
	conn, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return fmt.Errorf("capture_sync: connection id: %w", err)
	}
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return err
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
// missing or nearing expiry and registers/renews each against the configured
// Pub/Sub topic, advancing watch_expires_at. One connection's failure is logged
// and skipped (a revoked mailbox must not force the whole pass to retry); only a
// fleet-enumeration failure is returned (so River retries the tick). It mirrors
// gmailSyncWorker — the same DueConnections-shaped walk, keyed on the renewal
// deadline instead of the sync cursor.
type gmailWatchWorker struct {
	river.WorkerDefaults[GmailWatchArgs]
	registry    *capture.Registry
	topic       string
	renewWithin time.Duration
	log         *slog.Logger
}

func (w *gmailWatchWorker) Work(ctx context.Context, _ *river.Job[GmailWatchArgs]) error {
	due, enumErr := w.registry.DueWatches(ctx, "gmail", w.renewWithin)
	for _, d := range due {
		wsCtx := principal.WithWorkspaceID(ctx, d.Workspace.UUID)
		if err := w.registry.RenewWatch(wsCtx, d.ID, w.topic); err != nil {
			w.log.WarnContext(ctx, "gmail watch renewal failed", "connection", d.ID.String(), "err", err)
		}
	}
	return enumErr
}
