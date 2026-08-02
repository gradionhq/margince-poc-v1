// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The nightly reconcile for the interaction projection (CG-DDL-1 / ADR-0078).
//
// The consumer keeps the projection current as records change, and that covers
// everything an EVENT can express. It does not cover the one thing nothing
// emits an event for: the passage of time. A 90-day window count goes stale
// because a day passed, not because anybody did anything, so an interaction
// ageing out of the window changes a row that nothing will otherwise rewrite.
// That is the bounded-staleness contract the migration states — counts may be
// up to 24h over-inclusive — and this pass is what bounds it.
//
// It is also the corruption remedy. The projection carries no audit trail on
// purpose, because it holds no fact of its own; whatever state a missed event
// or a half-applied batch left behind, a rebuild from the base tables restores
// the only correct answer.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GraphEdgeReconcileArgs is the nightly pass's (empty) job payload.
type GraphEdgeReconcileArgs struct{}

// Kind is the River job kind for the interaction-projection reconcile.
func (GraphEdgeReconcileArgs) Kind() string { return "graph_edge_reconcile" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (GraphEdgeReconcileArgs) FleetWide() {}

// graphEdgeReconcileInterval is daily, matching the staleness bound the
// migration states. Tightening it would narrow the window at the cost of a
// full refold per workspace per run; loosening it would make the migration's
// stated contract a lie.
const graphEdgeReconcileInterval = 24 * time.Hour

// graphEdgeReconcileWorker rebuilds the projection for every workspace.
type graphEdgeReconcileWorker struct {
	river.WorkerDefaults[GraphEdgeReconcileArgs]
	pool  *pgxpool.Pool
	store *search.Store
	log   *slog.Logger
}

func newGraphEdgeReconcileWorker(pool *pgxpool.Pool, log *slog.Logger) *graphEdgeReconcileWorker {
	return &graphEdgeReconcileWorker{pool: pool, store: search.NewStore(pool), log: log}
}

// Work rebuilds each workspace's projection in its own transaction. Per
// workspace rather than globally, so one tenant's failure leaves the others
// reconciled — and so no single transaction spans the whole installation.
func (w *graphEdgeReconcileWorker) Work(ctx context.Context, _ *river.Job[GraphEdgeReconcileArgs]) error {
	workspaces, err := liveWorkspaceIDs(ctx, w.pool)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if err := w.reconcileWorkspace(ctx, ws); err != nil {
			w.log.WarnContext(ctx, "graph-edge reconcile: workspace rebuild failed",
				"workspace", ws.String(), "err", err)
		}
	}
	return nil
}

func (w *graphEdgeReconcileWorker) reconcileWorkspace(ctx context.Context, ws ids.UUID) error {
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:graph_edge_reconcile",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	// One transaction for the whole workspace: the rebuild clears and refills,
	// and a reader that arrived between those two statements would see an
	// empty graph. Inside a transaction they see the old one until the new one
	// commits.
	return database.WithWorkspaceTx(wsCtx, w.pool, func(tx pgx.Tx) error {
		return search.RebuildEdges(wsCtx, tx)
	})
}
