// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the AI-activity projection's two maintenance passes: the
// reconciler that re-asserts truth from the source tables, and the retention
// pass that drops settled occurrences past their window.
//
// They are two kinds rather than one because they fail differently and cost
// differently. The reconciler re-publishes onto the bus and its failure is a
// display that stays wrong; the retention pass is an indexed delete and its
// failure is a table that grows. Sharing a kind would make one's backlog the
// other's outage.

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// aiActivityReconcileBatch bounds one pass. A pass that announced everything it
// found would, on an installation with a long backlog, hold its transaction for
// as long as the backlog is deep; a bounded pass makes progress every tick and
// the tick is what carries the rest.
const aiActivityReconcileBatch = 500

// aiActivityRetentionWindow is how long a settled occurrence is kept.
//
// Wider than the rail needs — the rail shows today — because the paginated feed
// that follows pages backwards through exactly these rows, and a window
// narrowed to today's needs could only be widened by keeping history nobody
// kept.
const aiActivityRetentionWindow = 14 * 24 * time.Hour

// aiActivityReconcileActor and aiActivityRetentionActor are the principals the
// two passes' ledger rows carry. Separate strings because a reader looking at a
// system_log row should be able to tell which pass wrote it.
const (
	aiActivityReconcileActor = "system:ai_activity_reconcile"
	aiActivityRetentionActor = "system:ai_activity_retention"
)

// AIActivityReconcileArgs schedules one re-assertion of AI-activity truth from
// the source tables.
type AIActivityReconcileArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (AIActivityReconcileArgs) Kind() string { return "ai_activity_reconcile" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own. Held
// equal to api/jobs.yaml by TestArgsOwnedAttemptCapsMatchTheirDeclaration.
func (AIActivityReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// AIActivityRetentionArgs schedules one purge of settled AI-activity rows past
// their retention window.
type AIActivityRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (AIActivityRetentionArgs) Kind() string { return "ai_activity_retention" }

// InsertOpts carries the attempt cap the declaration publishes, for the same
// reason the reconciler's does.
func (AIActivityRetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// aiActivityReconcileWorker re-announces every source occurrence the projection
// could still be wrong about.
type aiActivityReconcileWorker struct {
	activities *activities.Store
	identity   *identity.Service
	now        func() time.Time
	log        *slog.Logger
}

func (w *aiActivityReconcileWorker) Work(ctx context.Context, _ *river.Job[AIActivityReconcileArgs]) error {
	passCtx, err := w.passContext(ctx, aiActivityReconcileActor)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	announced, err := w.activities.ReconcileExtractionActivity(passCtx, aiActivityReconcileBatch, w.now())
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if announced > 0 {
		w.log.InfoContext(passCtx, "ai activity reconcile: occurrences re-announced", "occurrences", announced)
	}
	return nil
}

// passContext binds what a write needs and the bus insists on: the
// installation's workspace, a system actor, and a correlation id, so every
// event this pass stages is traceable to the pass that staged it.
func (w *aiActivityReconcileWorker) passContext(ctx context.Context, actor string) (context.Context, error) {
	wsCtx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return nil, err
	}
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	return principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: actor,
	}), nil
}

// abandonedRouterGrace is how long past its own lease a live router occurrence
// must sit before the sweep closes it.
//
// DERIVED from what it must not race: the lease already covers the whole logical
// call, so a row still live past it has either been settled by a flush this
// sweep has not read yet, or never will be. traceWriteTimeout is the window that
// last flush is allowed, so waiting that long again plus a full retention tick
// means a row is only closed after the flush that could have settled it has
// certainly finished or certainly died.
const abandonedRouterGrace = 2 * time.Hour

// aiActivityRetentionWorker drops the settled occurrences nothing reads, and
// closes the live ones whose source will never settle them.
type aiActivityRetentionWorker struct {
	projection *aiactivity.Store
	identity   *identity.Service
	now        func() time.Time
	log        *slog.Logger
}

func (w *aiActivityRetentionWorker) Work(ctx context.Context, _ *river.Job[AIActivityRetentionArgs]) error {
	wsCtx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: aiActivityRetentionActor,
	})
	// Closing runs BEFORE purging, and the order is not cosmetic: closing moves
	// an abandoned row into the settled states, which is what makes it reachable
	// by the purge at all. Purging first would leave every row this sweep closes
	// waiting a whole extra tick for a lifetime it has already served.
	closed, err := w.projection.CloseAbandonedRouterRuns(wsCtx, w.now().Add(-abandonedRouterGrace))
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if closed > 0 {
		w.log.InfoContext(wsCtx, "ai activity retention: router occurrences closed after their lease ran out with no settle", "rows", closed)
	}
	purged, err := w.projection.PurgeSettledBefore(wsCtx, w.now().Add(-aiActivityRetentionWindow))
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if purged > 0 {
		w.log.InfoContext(wsCtx, "ai activity retention: settled occurrences purged", "rows", purged)
	}
	return nil
}
