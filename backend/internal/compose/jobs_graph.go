// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The background passes behind the relationship graph (ADR-0078), registered
// together because they are two halves of one guarantee: the backfill gives
// the projection a past to fold, and the reconcile keeps the fold true as time
// passes. Neither needs a model or a provider, so both run wherever the worker
// runs — "who on our team knows this contact" is a deterministic question
// about our own mail, and an installation with AI switched off still answers it.

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// addGraphJobs registers the graph workers and returns their periodic
// schedules for the caller to append.
func addGraphJobs(workers *river.Workers, pool *pgxpool.Pool, log *slog.Logger) []*river.PeriodicJob {
	river.AddWorker(workers, newParticipantBackfillWorker(pool, log))
	river.AddWorker(workers, newGraphEdgeReconcileWorker(pool, log))
	river.AddWorker(workers, newLinkedInRematchWorker(pool, people.NewStore(pool), log))
	return []*river.PeriodicJob{
		// The interaction-participant backfill: daily, run-on-start so an
		// installation upgrading into ACT-DDL-3 recovers its history on the
		// first boot rather than on the first mail that happens to arrive. It
		// stays periodic because a restore or an import can reintroduce
		// unattributed rows; a caught-up workspace costs one probe.
		river.NewPeriodicJob(river.PeriodicInterval(participantBackfillInterval),
			func() (river.JobArgs, *river.InsertOpts) { return ParticipantBackfillArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true}),
		// The projection reconcile: daily, because the 90-day window counts go
		// stale through the passage of time and nothing emits an event for
		// that. This pass is what bounds the staleness the migration promises,
		// and doubles as the corruption remedy.
		river.NewPeriodicJob(river.PeriodicInterval(graphEdgeReconcileInterval),
			func() (river.JobArgs, *river.InsertOpts) { return GraphEdgeReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true}),
		// The LinkedIn re-match: hourly, because the ghosts an export leaves
		// unmatched are waiting on people and accounts that mail capture is
		// still creating. Without it the upload-time pass is the only pass
		// there is, and a network imported on a new workspace matches almost
		// nothing forever.
		river.NewPeriodicJob(river.PeriodicInterval(linkedInRematchInterval),
			func() (river.JobArgs, *river.InsertOpts) { return LinkedInRematchArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}
