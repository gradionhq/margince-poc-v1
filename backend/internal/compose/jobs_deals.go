// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the deals module's two scheduled passes, alongside the
// per-module job files this package already keeps (jobs_capture.go,
// jobs_overlay.go). The adapters are the only code that knows about River;
// the deals correctors stay the River-agnostic seam.

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
)

// CloseDateSweepArgs schedules one close-date hygiene pass (INV-CLOSE-PAST).
type CloseDateSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CloseDateSweepArgs) Kind() string { return "close_date_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (CloseDateSweepArgs) FleetWide() {}

// FollowUpReconcileArgs schedules one overnight follow-up reconciliation
// pass (features/07 §8a).
type FollowUpReconcileArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (FollowUpReconcileArgs) Kind() string { return "follow_up_reconcile" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (FollowUpReconcileArgs) FleetWide() {}

// closeDateSweepWorker delegates a River job to the deals corrector.
type closeDateSweepWorker struct {
	river.WorkerDefaults[CloseDateSweepArgs]
	corrector *deals.CloseDateCorrector
}

func (w *closeDateSweepWorker) Work(ctx context.Context, _ *river.Job[CloseDateSweepArgs]) error {
	return w.corrector.Sweep(ctx)
}

// followUpReconcileWorker delegates a River job to the deals reconciler.
type followUpReconcileWorker struct {
	river.WorkerDefaults[FollowUpReconcileArgs]
	reconciler *deals.FollowUpReconciler
}

func (w *followUpReconcileWorker) Work(ctx context.Context, _ *river.Job[FollowUpReconcileArgs]) error {
	return w.reconciler.Reconcile(ctx)
}
