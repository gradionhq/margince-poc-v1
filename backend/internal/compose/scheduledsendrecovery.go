// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pass that finds a scheduled message nothing will ever wake.
//
// A scheduled row is armed by a River job and by nothing else. That is the right
// design — the row is the schedule and the job is a dumb alarm — but it has one
// failure the rest of the path cannot see: a job discarded after exhausting its
// attempts, or lost to an outage that spanned the whole ladder, leaves the row
// `scheduled` with no timer. Nothing wakes it, and the rep is told nothing,
// because being told is something the fire path does and the fire path never
// runs.
//
// So this re-arms it. Not "sends it" and not "holds it": it enqueues the alarm
// that was lost, and the ordinary fire path then makes the ordinary decision —
// send it, hold it for consent, hold it as a missed window. A sweep that decided
// anything itself would be a second send path, and the whole design of this
// feature is that there is one.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ScheduledSendRecoveryArgs carries nothing: the pass reads what is overdue
// rather than being told, because the rows it exists to find are exactly the
// ones nobody knows about.
type ScheduledSendRecoveryArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (ScheduledSendRecoveryArgs) Kind() string { return "comms_scheduled_send_recovery" }

// recoveryGrace is how overdue a message must be before this pass touches it.
//
// Comfortably past the send job's own retry ladder, so a message still being
// worked is left alone: re-arming one mid-flight is safe (the claim lock
// serializes them) but it would have this pass fighting the normal path on every
// run, which makes its log unreadable for the case it actually exists for.
const recoveryGrace = 30 * time.Minute

// recoveryBatch bounds one pass. A backlog is recovered across several runs
// rather than in one long transaction — the pass runs every quarter hour, and a
// sweep that tried to drain an outage's entire backlog at once would hold its
// slot for as long as the outage lasted.
const recoveryBatch = 100

// scheduledSendRecoveryWorker re-arms messages whose alarm is gone.
type scheduledSendRecoveryWorker struct {
	pool  *pgxpool.Pool
	store *activities.Store
	timer activities.ScheduleTimer
	log   *slog.Logger
}

func newScheduledSendRecoveryWorker(pool *pgxpool.Pool, store *activities.Store, timer activities.ScheduleTimer, log *slog.Logger) *scheduledSendRecoveryWorker {
	return &scheduledSendRecoveryWorker{pool: pool, store: store, timer: timer, log: log}
}

func (w *scheduledSendRecoveryWorker) Work(ctx context.Context, _ *river.Job[ScheduledSendRecoveryArgs]) error {
	overdue, err := w.store.OverdueScheduledSends(ctx, w.pool, recoveryGrace, recoveryBatch)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("comms_scheduled_send_recovery: reading overdue messages: %w", err))
	}
	if len(overdue) == 0 {
		return nil
	}
	// Logged at WARN because finding anything here means a timer was lost, which
	// an operator wants to know about even though the message is now recovered.
	w.log.WarnContext(ctx, "scheduled messages had no live timer and were re-armed",
		"count", len(overdue))

	for _, row := range overdue {
		if err := w.rearm(ctx, row); err != nil {
			// One unrecoverable message must not strand the rest of the batch:
			// they are independent, and the next pass retries this one anyway.
			w.log.ErrorContext(ctx, "re-arming a scheduled message failed",
				"scheduled_send", row.ID, "err", err)
		}
	}
	return nil
}

// rearm enqueues the alarm this message lost, due now: its moment has already
// passed, so the fire path decides immediately what to do about that — including
// holding it as a missed window, which is the honest outcome for a message an
// outage carried past its time.
func (w *scheduledSendRecoveryWorker) rearm(ctx context.Context, row activities.OverdueScheduledSend) error {
	// The message's OWN workspace, bound here rather than taken from this job's
	// context: the pass has no tenant — it sweeps the installation for rows
	// nobody is watching — and both the claim below and the alarm it enqueues
	// refuse without one. Binding per message is what lets one pass recover
	// messages belonging to different workspaces.
	return w.store.RearmScheduledSend(
		sendWorkerScope(principal.WithWorkspaceID(ctx, row.Workspace)), row.ID, w.timer)
}

// addScheduledSendRecoveryJob registers the pass and its cadence. Gated on the
// same delivery machinery the send worker is: a role that cannot fire a message
// has no business re-arming one.
func addScheduledSendRecoveryJob(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	if cfg.SendDelivery == nil {
		return nil
	}
	inserter, err := jobs.NewInserter(pool, log)
	if err != nil {
		// A role that cannot open an inserter cannot arm anything, which the
		// send path itself would also fail on. Registering a worker that could
		// only error would make the census claim a recovery this build cannot
		// perform.
		log.Error("scheduled-send recovery not registered: no job inserter", "err", err)
		return nil
	}
	addDeclaredWorker[ScheduledSendRecoveryArgs](reg, newScheduledSendRecoveryWorker(
		pool, sendStore(pool, SendPath{}), NewScheduleTimer(inserter), log))
	return periodicFor(cfg, ScheduledSendRecoveryArgs{})
}
