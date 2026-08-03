// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The signal producers' River wiring (SIG-F-3): one hourly pass per workspace
// that runs both halves — the deterministic ghosted-thread rule, then the
// model read of the settled conversations.
//
// They ride ONE job on purpose. Both write signals about the same accounts,
// and a rep reading the page should not see half an account's signals because
// two schedules drifted apart. The deterministic half runs first and always:
// it costs one query and no model call, so an installation with no AI
// configured still gets the signals a comparison can produce.
//
// Job args and worker adapters only — the engines stay River-agnostic.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SignalScanArgs runs one fleet-wide signal-producer pass.
type SignalScanArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (SignalScanArgs) Kind() string { return "signal_scan" }

// FleetWide marks this a dispatcher: it enumerates and enqueues, and does no
// tenant work of its own (jobs.FleetWide).
func (SignalScanArgs) FleetWide() {}

// SignalScanWorkspaceArgs is one workspace's signal-producer pass.
type SignalScanWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (SignalScanWorkspaceArgs) Kind() string { return "signal_scan_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a SignalScanWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// signalScanWorker is the dispatcher.
type signalScanWorker struct {
	river.WorkerDefaults[SignalScanArgs]
	pool *pgxpool.Pool
}

func (w *signalScanWorker) Work(ctx context.Context, _ *river.Job[SignalScanArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(aiCaptureQueue, sweepWorkspaceMaxAttempts),
		func(ws ids.UUID) river.JobArgs { return SignalScanWorkspaceArgs{Workspace: ws} }))
}

// signalScanWorkspaceWorker runs both producers for one workspace.
//
// The extractor is nil when no model lane is configured. That is not a
// degraded state to log about on every tick — it is an installation that
// bought no model, and the deterministic half is the whole product for it.
type signalScanWorkspaceWorker struct {
	river.WorkerDefaults[SignalScanWorkspaceArgs]
	pool      *pgxpool.Pool
	extractor *SignalExtractor
	now       func() time.Time
	log       *slog.Logger
}

func (w *signalScanWorkspaceWorker) Work(ctx context.Context, job *river.Job[SignalScanWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// The producer is the acting principal: every signal it writes carries
	// agent: provenance, and a reader can tell it from a human's own note.
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:signal-scan",
	})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	wsID := ids.From[ids.WorkspaceKind](job.Args.Workspace)

	now := w.now()
	var ghosted int
	if err := database.WithWorkspaceTx(wsCtx, w.pool, func(tx pgx.Tx) error {
		written, err := WriteGhostedSignals(wsCtx, tx, wsID, now)
		ghosted = written
		return err
	}); err != nil {
		return jobs.FaultContext(ctx, err)
	}

	read := 0
	if w.extractor != nil {
		read, err = w.extractor.RunWorkspace(wsCtx, wsID)
		if err != nil {
			return jobs.FaultContext(ctx, err)
		}
	}
	if ghosted+read > 0 {
		w.log.InfoContext(wsCtx, "signal scan: raised signals", "ghosted", ghosted, "extracted", read)
	}
	return nil
}

// newSignalExtractorIfConfigured builds the model half of the pass, or nil
// when no lane is bound. The nil IS the wiring: the worker runs the
// deterministic half either way, so an unconfigured installation gets the
// signals a comparison can produce and none of the ones only a reader can.
func newSignalExtractorIfConfigured(pool *pgxpool.Pool, brain completer, log *slog.Logger) *SignalExtractor {
	if brain == nil {
		return nil
	}
	return NewSignalExtractor(pool, brain, time.Now, log)
}
