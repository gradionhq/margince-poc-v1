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
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// addGraphJobs registers the graph workers and returns their periodic
// schedules for the caller to append.
//
// All three passes register unconditionally, which is the point the file
// comment makes: none of them needs a model or a provider, so there is nothing
// for a deployment to leave unconfigured. Their cadences are api/jobs.yaml's.
func addGraphJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	// Each pass is a dispatcher plus a workspace worker; only the dispatcher
	// gets a periodic entry, and the workspace worker reuses its wiring.
	participants := newParticipantBackfillWorker(pool, log)
	addDeclaredWorker[ParticipantBackfillArgs](reg, participants)
	addDeclaredWorker[ParticipantBackfillWorkspaceArgs](reg, &participantBackfillWorkspaceWorker{participantBackfillWorker: participants})
	graphEdges := newGraphEdgeReconcileWorker(pool, log)
	addDeclaredWorker[GraphEdgeReconcileArgs](reg, graphEdges)
	addDeclaredWorker[GraphEdgeWorkspaceArgs](reg, &graphEdgeWorkspaceWorker{graphEdgeReconcileWorker: graphEdges})
	rematch := newLinkedInRematchWorker(pool, people.NewStore(pool), identity.NewService(pool), log)
	addDeclaredWorker[LinkedInRematchArgs](reg, rematch)
	addDeclaredWorker[LinkedInRematchWorkspaceArgs](reg, &linkedInRematchWorkspaceWorker{linkedInRematchWorker: rematch})
	return slices.Concat(
		periodicFor(cfg, ParticipantBackfillArgs{}),
		periodicFor(cfg, GraphEdgeReconcileArgs{}),
		periodicFor(cfg, LinkedInRematchArgs{}),
	)
}
