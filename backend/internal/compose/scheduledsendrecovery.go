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
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	identity *identity.Service
	store    *activities.Store
	timer    activities.ScheduleTimer
	log      *slog.Logger
}

func newScheduledSendRecoveryWorker(idsvc *identity.Service, store *activities.Store, timer activities.ScheduleTimer, log *slog.Logger) *scheduledSendRecoveryWorker {
	return &scheduledSendRecoveryWorker{identity: idsvc, store: store, timer: timer, log: log}
}

func (w *scheduledSendRecoveryWorker) Work(ctx context.Context, _ *river.Job[ScheduledSendRecoveryArgs]) error {
	// The installation's own workspace, on the context rather than left to the
	// store's handle to resolve. The handle resolves it either way, so a context
	// that disagreed would put the transaction's app.workspace_id and the
	// workspace this code believes it is acting in out of step — and a later
	// writer inside this transaction would lock under one and stamp under the
	// other. installationJobCtx is the spelling every other tenant-less pass uses.
	ctx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("comms_scheduled_send_recovery: resolving the installation: %w", err))
	}
	ctx = sendWorkerScope(ctx)

	overdue, err := w.store.OverdueScheduledSends(ctx, recoveryGrace, recoveryBatch)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("comms_scheduled_send_recovery: reading overdue messages: %w", err))
	}
	if len(overdue) == 0 {
		return nil
	}

	var rearmed, failed int
	for _, id := range overdue {
		if err := w.rearm(ctx, id); err != nil {
			// One unrecoverable message must not strand the rest of the batch:
			// they are independent, and each is claimed on its own.
			failed++
			w.log.ErrorContext(ctx, "re-arming a scheduled message failed",
				"scheduled_send", id, "err", err)
			continue
		}
		rearmed++
	}
	// After the loop and counting outcomes, so the line cannot claim a recovery
	// that did not happen. WARN because reaching here at all means timers were
	// lost, which an operator wants to know even though the messages are back.
	// `failed` is the number this pass could not recover: a count that stays
	// non-zero across passes is a row no cadence will heal, and the only place
	// that is visible.
	w.log.WarnContext(ctx, "scheduled messages had no live timer",
		"found", len(overdue), "rearmed", rearmed, "failed", failed)
	return nil
}

// rearm enqueues the alarm this message lost, due now: its moment has already
// passed, so the fire path decides immediately what to do about that — including
// holding it as a missed window, which is the honest outcome for a message an
// outage carried past its time.
func (w *scheduledSendRecoveryWorker) rearm(ctx context.Context, id ids.UUID) error {
	return w.store.RearmScheduledSend(ctx, id, w.timer)
}

// addScheduledSendRecoveryJob registers the pass and its cadence.
//
// Gated on exactly what the comms_scheduled_send worker is gated on, because
// that worker is what this pass enqueues into. A role holding only one of the
// two would run the sweep and file alarms nothing consumes — and ScheduledSendArgs
// carries no UniqueOpts, so they would accumulate in river_job every quarter
// hour rather than collapsing.
func addScheduledSendRecoveryJob(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	if cfg.SendRegistry == nil || cfg.SendDelivery == nil {
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
		identity.NewService(pool), sendStore(pool, SendPath{}), NewScheduleTimer(inserter), log))
	return periodicFor(cfg, ScheduledSendRecoveryArgs{})
}
